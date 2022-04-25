// SPDX-License-Identifier: AGPL-3.0-only

package ingester

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveworks/common/tracing"

	"github.com/grafana/dskit/tenant"

	"github.com/grafana/mimir/pkg/ingester/client"
	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/util/activitytracker"
)

// ActivityTrackerWrapper is a wrapper around Ingester that adds queries to activity tracker.
type ActivityTrackerWrapper struct {
	ing     *Ingester
	tracker *activitytracker.ActivityTracker
}

func NewIngesterActivityTracker(ing *Ingester, tracker *activitytracker.ActivityTracker) *ActivityTrackerWrapper {
	return &ActivityTrackerWrapper{
		ing:     ing,
		tracker: tracker,
	}
}

func (i *ActivityTrackerWrapper) Push(ctx context.Context, request *mimirpb.WriteRequest) (*mimirpb.WriteResponse, error) {
	// No tracking in Push
	return i.ing.Push(ctx, request)
}

func (i *ActivityTrackerWrapper) PushWithCleanup(ctx context.Context, w *mimirpb.WriteRequest, c func()) (*mimirpb.WriteResponse, error) {
	// No tracking in PushWithCleanup
	return i.ing.PushWithCleanup(ctx, w, c)
}

func (i *ActivityTrackerWrapper) QueryStream(request *client.QueryRequest, server client.Ingester_QueryStreamServer) error {
	ix := i.tracker.Insert(func() string {
		return requestActivity(server.Context(), "Ingester/QueryStream", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.QueryStream(request, server)
}

func (i *ActivityTrackerWrapper) QueryExemplars(ctx context.Context, request *client.ExemplarQueryRequest) (*client.ExemplarQueryResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/QueryExemplars", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.QueryExemplars(ctx, request)
}

func (i *ActivityTrackerWrapper) LabelValues(ctx context.Context, request *client.LabelValuesRequest) (*client.LabelValuesResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/LabelValues", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.LabelValues(ctx, request)
}

func (i *ActivityTrackerWrapper) LabelNames(ctx context.Context, request *client.LabelNamesRequest) (*client.LabelNamesResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/LabelNames", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.LabelNames(ctx, request)
}

func (i *ActivityTrackerWrapper) UserStats(ctx context.Context, request *client.UserStatsRequest) (*client.UserStatsResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/UserStats", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.UserStats(ctx, request)
}

func (i *ActivityTrackerWrapper) AllUserStats(ctx context.Context, request *client.UserStatsRequest) (*client.UsersStatsResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/AllUserStats", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.AllUserStats(ctx, request)
}

func (i *ActivityTrackerWrapper) MetricsForLabelMatchers(ctx context.Context, request *client.MetricsForLabelMatchersRequest) (*client.MetricsForLabelMatchersResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/MetricsForLabelMatchers", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.MetricsForLabelMatchers(ctx, request)
}

func (i *ActivityTrackerWrapper) MetricsMetadata(ctx context.Context, request *client.MetricsMetadataRequest) (*client.MetricsMetadataResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(ctx, "Ingester/MetricsMetadata", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.MetricsMetadata(ctx, request)
}

func (i *ActivityTrackerWrapper) LabelNamesAndValues(request *client.LabelNamesAndValuesRequest, server client.Ingester_LabelNamesAndValuesServer) error {
	ix := i.tracker.Insert(func() string {
		return requestActivity(server.Context(), "Ingester/LabelNamesAndValues", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.LabelNamesAndValues(request, server)
}

func (i *ActivityTrackerWrapper) LabelValuesCardinality(request *client.LabelValuesCardinalityRequest, server client.Ingester_LabelValuesCardinalityServer) error {
	ix := i.tracker.Insert(func() string {
		return requestActivity(server.Context(), "Ingester/LabelValuesCardinality", request)
	})
	defer i.tracker.Delete(ix)

	return i.ing.LabelValuesCardinality(request, server)
}

func (i *ActivityTrackerWrapper) UploadBackfillFile(stream client.Ingester_UploadBackfillFileServer) error {
	ix := i.tracker.Insert(func() string {
		return requestActivity(context.Background(), "Ingester/UploadBackfillFile", stream)
	})
	defer i.tracker.Delete(ix)

	return i.ing.UploadBackfillFile(stream)
}

func (i *ActivityTrackerWrapper) FinishBackfill(ctx context.Context, req *mimirpb.FinishBackfillRequest) (*mimirpb.FinishBackfillResponse, error) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(context.Background(), "Ingester/FinishBackfill", req)
	})
	defer i.tracker.Delete(ix)

	return i.ing.FinishBackfill(ctx, req)
}

func (i *ActivityTrackerWrapper) FlushHandler(w http.ResponseWriter, r *http.Request) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(r.Context(), "Ingester/FlushHandler", nil)
	})
	defer i.tracker.Delete(ix)

	i.ing.FlushHandler(w, r)
}

func (i *ActivityTrackerWrapper) ShutdownHandler(w http.ResponseWriter, r *http.Request) {
	ix := i.tracker.Insert(func() string {
		return requestActivity(r.Context(), "Ingester/ShutdownHandler", nil)
	})
	defer i.tracker.Delete(ix)

	i.ing.ShutdownHandler(w, r)
}

func requestActivity(ctx context.Context, name string, req interface{}) string {
	userID, _ := tenant.TenantID(ctx)
	traceID, _ := tracing.ExtractSampledTraceID(ctx)
	return fmt.Sprintf("%s: user=%q trace=%q request=%v", name, userID, traceID, req)
}
