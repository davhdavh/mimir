// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/util/push/push.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package distributor

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/httpgrpc"
	"github.com/grafana/dskit/httpgrpc/server"
	"github.com/grafana/dskit/middleware"
	"github.com/grafana/dskit/tenant"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"

	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/util"
	"github.com/grafana/mimir/pkg/util/globalerror"
	utillog "github.com/grafana/mimir/pkg/util/log"
	"github.com/grafana/mimir/pkg/util/validation"
)

// PushFunc defines the type of the push. It is similar to http.HandlerFunc.
type PushFunc func(ctx context.Context, req *Request) error

// parserFunc defines how to read the body the request from an HTTP request
type parserFunc func(ctx context.Context, r *http.Request, maxSize int, skipLabelNameValidation bool, buffer []byte, logger log.Logger) (mimirpb.IWriteRequest, []byte, error)

// Wrap a slice in a struct so we can store a pointer in sync.Pool
type bufHolder struct {
	buf []byte
}

var (
	bufferPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, 256*1024))
		},
	}
	errRetryBaseLessThanOneSecond    = errors.New("retry base duration should not be less than 1 second")
	errNonPositiveMaxBackoffExponent = errors.New("max backoff exponent should be a positive value")
)

const (
	SkipLabelNameValidationHeader = "X-Mimir-SkipLabelNameValidation"
	statusClientClosedRequest     = 499
)

type RetryConfig struct {
	Enabled            bool `yaml:"enabled" category:"experimental"`
	BaseSeconds        int  `yaml:"base_seconds" category:"experimental"`
	MaxBackoffExponent int  `yaml:"max_backoff_exponent" category:"experimental"`
}

// RegisterFlags adds the flags required to config this to the given FlagSet.
func (cfg *RetryConfig) RegisterFlags(f *flag.FlagSet) {
	f.BoolVar(&cfg.Enabled, "distributor.retry-after-header.enabled", false, "Enabled controls inclusion of the Retry-After header in the response: true includes it for client retry guidance, false omits it.")
	f.IntVar(&cfg.BaseSeconds, "distributor.retry-after-header.base-seconds", 3, "Base duration in seconds for calculating the Retry-After header in responses to 429/5xx errors.")
	f.IntVar(&cfg.MaxBackoffExponent, "distributor.retry-after-header.max-backoff-exponent", 5, "Sets the upper limit on the number of Retry-Attempt considered for calculation. It caps the Retry-Attempt header without rejecting additional attempts, controlling exponential backoff calculations. For example, when the base-seconds is set to 3 and max-backoff-exponent to 5, the maximum retry duration would be 3 * 2^5 = 96 seconds.")
}

func (cfg *RetryConfig) Validate() error {
	if cfg.BaseSeconds < 1 {
		return errRetryBaseLessThanOneSecond
	}
	if cfg.MaxBackoffExponent < 1 {
		return errNonPositiveMaxBackoffExponent
	}
	return nil
}

// Handler is a http.Handler which accepts mimirpb.IWriteRequests.
func Handler(
	maxRecvMsgSize int,
	sourceIPs *middleware.SourceIPExtractor,
	allowSkipLabelNameValidation bool,
	limits *validation.Overrides,
	retryCfg RetryConfig,
	push PushFunc,
	logger log.Logger,
) http.Handler {
	return handler(maxRecvMsgSize, sourceIPs, allowSkipLabelNameValidation, limits, retryCfg, push, logger, func(ctx context.Context, r *http.Request, maxRecvMsgSize int, skipLabelNameValidation bool, dst []byte, _ log.Logger) (mimirpb.IWriteRequest, []byte, error) {
		bufHolder := bufferPool.Get().(*bufHolder)
		buf := bufHolder.buf
		var req mimirpb.PreallocWriteRequest
		res, err := util.ParseProtoReader(ctx, r.Body, int(r.ContentLength), maxRecvMsgSize, buf, &req, util.RawSnappy)
		if err != nil {
			if errors.Is(err, util.MsgSizeTooLargeErr{}) {
				err = distributorMaxWriteMessageSizeErr{actual: int(r.ContentLength), limit: maxRecvMsgSize}
			}
			bufferPool.Put(bufHolder)
			return nil, nil, err
		}

		// If decoding allocated a bigger buffer, put that one back in the pool.
		if buf = buf[:cap(buf)]; len(buf) > len(bufHolder.buf) {
			bufHolder.buf = buf
		}
		req.SkipLabelNameValidation = skipLabelNameValidation
		req.bufHolder = bufHolder
		return req, res, nil
	})
}

// mimirRequest implements mimirpb.IWriteRequest.
type mimirRequest struct {
	wrapped   *mimirpb.PreallocWriteRequest
	bufHolder *bufHolder
}

func (r *mimirRequest) Cleanup() {
	mimirpb.ReuseSlice(r.wrapped.Timeseries)
	bufferPool.Put(r.bufHolder)
}

func (r *mimirRequest) PruneTimeseries(tenantID string, relabelConfigs []*relabel.Config, dropLabels string) {
	var removeTsIndexes []int
	lb := labels.NewBuilder(labels.EmptyLabels())
	for tsIdx := 0; tsIdx < len(r.Timeseries); tsIdx++ {
		ts := r.Timeseries[tsIdx]

		if len(relabelConfigs) > 0 {
			mimirpb.FromLabelAdaptersToBuilder(ts.Labels, lb)
			lb.Set(metaLabelTenantID, tenantID)
			keep := relabel.ProcessBuilder(lb, relabelConfigs...)
			if !keep {
				removeTsIndexes = append(removeTsIndexes, tsIdx)
				continue
			}
			lb.Del(metaLabelTenantID)
			r.Timeseries[tsIdx].SetLabels(mimirpb.FromBuilderToLabelAdapters(lb, ts.Labels))
		}

		for _, labelName := range dropLabels {
			r.Timeseries[tsIdx].RemoveLabel(labelName)
		}

		// Prometheus strips empty values before storing; drop them now, before sharding to ingesters.
		r.Timeseries[tsIdx].RemoveEmptyLabelValues()

		if len(ts.Labels) == 0 {
			removeTsIndexes = append(removeTsIndexes, tsIdx)
			continue
		}

		// We rely on sorted labels in different places:
		// 1) When computing token for labels, and sharding by all labels. Here different order of labels returns
		// different tokens, which is bad.
		// 2) In validation code, when checking for duplicate label names. As duplicate label names are rejected
		// later in the validation phase, we ignore them here.
		// 3) Ingesters expect labels to be sorted in the Push request.
		r.Timeseries[tsIdx].SortLabelsIfNeeded()
	}

	if len(removeTsIndexes) > 0 {
		for _, removeTsIndex := range removeTsIndexes {
			mimirpb.ReusePreallocTimeseries(&r.Timeseries[removeTsIndex])
		}
		r.Timeseries = util.RemoveSliceIndexes(r.Timeseries, removeTsIndexes)
	}

}

type distributorMaxWriteMessageSizeErr struct {
	actual, limit int
}

func (e distributorMaxWriteMessageSizeErr) Error() string {
	msgSizeDesc := fmt.Sprintf(" of %d bytes", e.actual)
	if e.actual < 0 {
		msgSizeDesc = ""
	}
	return globalerror.DistributorMaxWriteMessageSize.MessageWithPerInstanceLimitConfig(fmt.Sprintf("the incoming push request has been rejected because its message size%s is larger than the allowed limit of %d bytes", msgSizeDesc, e.limit), "distributor.max-recv-msg-size")
}

func handler(
	maxRecvMsgSize int,
	sourceIPs *middleware.SourceIPExtractor,
	allowSkipLabelNameValidation bool,
	limits *validation.Overrides,
	retryCfg RetryConfig,
	push PushFunc,
	logger log.Logger,
	parser parserFunc,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := utillog.WithContext(ctx, logger)
		if sourceIPs != nil {
			source := sourceIPs.Get(r)
			if source != "" {
				ctx = util.AddSourceIPsToOutgoingContext(ctx, source)
				logger = utillog.WithSourceIPs(source, logger)
			}
		}
		supplier := func() (mimirpb.IWriteRequest, error) {
			skipLabelNameValidation := allowSkipLabelNameValidation && req.SkipLabelNameValidation && r.Header.Get(SkipLabelNameValidationHeader) == "true"
			req, err := parser(ctx, r, maxRecvMsgSize, skipLabelNameValidation, logger)
			if err != nil {
				// Check for httpgrpc error, default to client error if parsing failed
				if _, ok := httpgrpc.HTTPResponseFromError(err); !ok {
					err = httpgrpc.Errorf(http.StatusBadRequest, err.Error())
				}

				return nil, nil, err
			}

			return req, nil
		}
		req := newRequest(supplier)
		if err := push(ctx, req); err != nil {
			if errors.Is(err, context.Canceled) {
				http.Error(w, err.Error(), statusClientClosedRequest)
				level.Warn(logger).Log("msg", "push request canceled", "err", err)
				return
			}
			var (
				code int
				msg  string
			)
			if resp, ok := httpgrpc.HTTPResponseFromError(err); ok {
				code, msg = int(resp.Code), string(resp.Body)
			} else {
				code, msg = toHTTPStatus(ctx, err, limits), err.Error()
			}
			if code != 202 {
				level.Error(logger).Log("msg", "push error", "err", err)
			}
			addHeaders(w, err, r, code, retryCfg)
			http.Error(w, msg, code)
		}
	})
}

func calculateRetryAfter(retryAttemptHeader string, baseSeconds int, maxBackoffExponent int) string {
	retryAttempt, err := strconv.Atoi(retryAttemptHeader)
	// If retry-attempt is not valid, set it to default 1
	if err != nil || retryAttempt < 1 {
		retryAttempt = 1
	}
	if retryAttempt > maxBackoffExponent {
		retryAttempt = maxBackoffExponent
	}
	var minSeconds, maxSeconds int64
	minSeconds = int64(baseSeconds) << (retryAttempt - 1)
	maxSeconds = int64(minSeconds) << 1

	delaySeconds := minSeconds + rand.Int63n(maxSeconds-minSeconds)

	return strconv.FormatInt(delaySeconds, 10)
}

// toHTTPStatus converts the given error into an appropriate HTTP status corresponding
// to that error, if the error is one of the errors from this package. Otherwise, an
// http.StatusInternalServerError is returned.
func toHTTPStatus(ctx context.Context, pushErr error, limits *validation.Overrides) int {
	if errors.Is(pushErr, context.DeadlineExceeded) {
		return http.StatusInternalServerError
	}

	var distributorErr distributorError
	if errors.As(pushErr, &distributorErr) {
		switch distributorErr.errorCause() {
		case mimirpb.BAD_DATA:
			return http.StatusBadRequest
		case mimirpb.INGESTION_RATE_LIMITED, mimirpb.REQUEST_RATE_LIMITED:
			serviceOverloadErrorEnabled := false
			userID, err := tenant.TenantID(ctx)
			if err == nil {
				serviceOverloadErrorEnabled = limits.ServiceOverloadStatusCodeOnRateLimitEnabled(userID)
			}
			// Return a 429 or a 529 here depending on configuration to tell the client it is going too fast.
			// Client may discard the data or slow down and re-send.
			// Prometheus v2.26 added a remote-write option 'retry_on_http_429'.
			if serviceOverloadErrorEnabled {
				return StatusServiceOverloaded
			}
			return http.StatusTooManyRequests
		case mimirpb.REPLICAS_DID_NOT_MATCH:
			return http.StatusAccepted
		case mimirpb.TOO_MANY_CLUSTERS:
			return http.StatusBadRequest
		case mimirpb.TSDB_UNAVAILABLE:
			return http.StatusServiceUnavailable
		case mimirpb.CIRCUIT_BREAKER_OPEN:
			return http.StatusServiceUnavailable
		}
	}

	return http.StatusInternalServerError
}

func addHeaders(w http.ResponseWriter, err error, r *http.Request, responseCode int, retryCfg RetryConfig) {
	var doNotLogError middleware.DoNotLogError
	if errors.As(err, &doNotLogError) {
		w.Header().Set(server.DoNotLogErrorHeaderKey, "true")
	}

	if responseCode == http.StatusTooManyRequests || responseCode/100 == 5 {
		if retryCfg.Enabled {
			retryAttemptHeader := r.Header.Get("Retry-Attempt")
			retrySeconds := calculateRetryAfter(retryAttemptHeader, retryCfg.BaseSeconds, retryCfg.MaxBackoffExponent)
			w.Header().Set("Retry-After", retrySeconds)
			if sp := opentracing.SpanFromContext(r.Context()); sp != nil {
				sp.SetTag("retry-after", retrySeconds)
				sp.SetTag("retry-attempt", retryAttemptHeader)
			}
		}
	}
}
