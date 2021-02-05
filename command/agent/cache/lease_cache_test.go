package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/vault/command/agent/cache/cachememdb"
	"github.com/hashicorp/vault/command/agent/cache/persistcache"
	"github.com/ryboe/q"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-test/deep"
	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/sdk/helper/consts"
	"github.com/hashicorp/vault/sdk/helper/logging"
)

func testNewLeaseCache(t *testing.T, responses []*SendResponse, storage persistcache.Storage) *LeaseCache {
	t.Helper()

	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	lc, err := NewLeaseCache(&LeaseCacheConfig{
		Client:      client,
		BaseContext: context.Background(),
		Proxier:     newMockProxier(responses),
		Logger:      logging.NewVaultLogger(hclog.Trace).Named("cache.leasecache"),
		Storage:     storage,
	})
	if err != nil {
		t.Fatal(err)
	}

	return lc
}

func TestCache_ComputeIndexID(t *testing.T) {
	type args struct {
		req *http.Request
	}
	tests := []struct {
		name    string
		req     *SendRequest
		want    string
		wantErr bool
	}{
		{
			"basic",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "test",
					},
				},
			},
			"7b5db388f211fd9edca8c6c254831fb01ad4e6fe624dbb62711f256b5e803717",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeIndexID(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("actual_error: %v, expected_error: %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, string(tt.want)) {
				t.Errorf("bad: index id; actual: %q, expected: %q", got, string(tt.want))
			}
		})
	}
}

func TestLeaseCache_EmptyToken(t *testing.T) {
	responses := []*SendResponse{
		newTestSendResponse(http.StatusCreated, `{"value": "invalid", "auth": {"client_token": "testtoken"}}`),
	}
	lc := testNewLeaseCache(t, responses, nil)

	// Even if the send request doesn't have a token on it, a successful
	// cacheable response should result in the index properly getting populated
	// with a token and memdb shouldn't complain while inserting the index.
	urlPath := "http://example.com/v1/sample/api"
	sendReq := &SendRequest{
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err := lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatalf("expected a non empty response")
	}
}

func TestLeaseCache_SendCacheable(t *testing.T) {
	// Emulate 2 responses from the api proxy. One returns a new token and the
	// other returns a lease.
	responses := []*SendResponse{
		newTestSendResponse(http.StatusCreated, `{"auth": {"client_token": "testtoken", "renewable": true}}`),
		newTestSendResponse(http.StatusOK, `{"lease_id": "foo", "renewable": true, "data": {"value": "foo"}}`),
	}

	lc := testNewLeaseCache(t, responses, nil)
	// Register an token so that the token and lease requests are cached
	lc.RegisterAutoAuthToken("autoauthtoken")

	// Make a request. A response with a new token is returned to the lease
	// cache and that will be cached.
	urlPath := "http://example.com/v1/sample/api"
	sendReq := &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err := lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[0].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Send the same request again to get the cached response
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[0].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Modify the request a little bit to ensure the second response is
	// returned to the lease cache.
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input_changed"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[1].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Make the same request again and ensure that the same response is returned
	// again.
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input_changed"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[1].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}
}

func TestLeaseCache_SendNonCacheable(t *testing.T) {
	responses := []*SendResponse{
		newTestSendResponse(http.StatusOK, `{"value": "output"}`),
		newTestSendResponse(http.StatusNotFound, `{"value": "invalid"}`),
		newTestSendResponse(http.StatusOK, `<html>Hello</html>`),
		newTestSendResponse(http.StatusTemporaryRedirect, ""),
	}

	lc := testNewLeaseCache(t, responses, nil)

	// Send a request through the lease cache which is not cacheable (there is
	// no lease information or auth information in the response)
	sendReq := &SendRequest{
		Request: httptest.NewRequest("GET", "http://example.com", strings.NewReader(`{"value": "input"}`)),
	}
	resp, err := lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response, responses[0].Response); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Since the response is non-cacheable, the second response will be
	// returned.
	sendReq = &SendRequest{
		Token:   "foo",
		Request: httptest.NewRequest("GET", "http://example.com", strings.NewReader(`{"value": "input"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response, responses[1].Response); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Since the response is non-cacheable, the third response will be
	// returned.
	sendReq = &SendRequest{
		Token:   "foo",
		Request: httptest.NewRequest("GET", "http://example.com", nil),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response, responses[2].Response); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Since the response is non-cacheable, the fourth response will be
	// returned.
	sendReq = &SendRequest{
		Token:   "foo",
		Request: httptest.NewRequest("GET", "http://example.com", nil),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response, responses[3].Response); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}
}

func TestLeaseCache_SendNonCacheableNonTokenLease(t *testing.T) {
	// Create the cache
	responses := []*SendResponse{
		newTestSendResponse(http.StatusOK, `{"value": "output", "lease_id": "foo"}`),
		newTestSendResponse(http.StatusCreated, `{"value": "invalid", "auth": {"client_token": "testtoken"}}`),
	}
	lc := testNewLeaseCache(t, responses, nil)

	// Send a request through lease cache which returns a response containing
	// lease_id. Response will not be cached because it doesn't belong to a
	// token that is managed by the lease cache.
	urlPath := "http://example.com/v1/sample/api"
	sendReq := &SendRequest{
		Token:   "foo",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err := lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response, responses[0].Response); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	idx, err := lc.db.Get(cachememdb.IndexNameRequestPath, "root/", urlPath)
	if err != nil {
		t.Fatal(err)
	}
	if idx != nil {
		t.Fatalf("expected nil entry, got: %#v", idx)
	}

	// Verify that the response is not cached by sending the same request and
	// by expecting a different response.
	sendReq = &SendRequest{
		Token:   "foo",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response, responses[1].Response); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	idx, err = lc.db.Get(cachememdb.IndexNameRequestPath, "root/", urlPath)
	if err != nil {
		t.Fatal(err)
	}
	if idx != nil {
		t.Fatalf("expected nil entry, got: %#v", idx)
	}
}

func TestLeaseCache_HandleCacheClear(t *testing.T) {
	lc := testNewLeaseCache(t, nil, nil)

	handler := lc.HandleCacheClear(context.Background())
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Test missing body, should return 400
	resp, err := http.Post(ts.URL, "application/json", nil)
	if err != nil {
		t.Fatal()
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status code mismatch: expected = %v, got = %v", http.StatusBadRequest, resp.StatusCode)
	}

	testCases := []struct {
		name               string
		reqType            string
		reqValue           string
		expectedStatusCode int
	}{
		{
			"invalid_type",
			"foo",
			"",
			http.StatusBadRequest,
		},
		{
			"invalid_value",
			"",
			"bar",
			http.StatusBadRequest,
		},
		{
			"all",
			"all",
			"",
			http.StatusOK,
		},
		{
			"by_request_path",
			"request_path",
			"foo",
			http.StatusOK,
		},
		{
			"by_token",
			"token",
			"foo",
			http.StatusOK,
		},
		{
			"by_lease",
			"lease",
			"foo",
			http.StatusOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := fmt.Sprintf("{\"type\": \"%s\", \"value\": \"%s\"}", tc.reqType, tc.reqValue)
			resp, err := http.Post(ts.URL, "application/json", strings.NewReader(reqBody))
			if err != nil {
				t.Fatal(err)
			}
			if tc.expectedStatusCode != resp.StatusCode {
				t.Fatalf("status code mismatch: expected = %v, got = %v", tc.expectedStatusCode, resp.StatusCode)
			}
		})
	}
}

func TestCache_DeriveNamespaceAndRevocationPath(t *testing.T) {
	tests := []struct {
		name             string
		req              *SendRequest
		wantNamespace    string
		wantRelativePath string
	}{
		{
			"non_revocation_full_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/ns1/sys/mounts",
					},
				},
			},
			"root/",
			"/v1/ns1/sys/mounts",
		},
		{
			"non_revocation_relative_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/sys/mounts",
					},
					Header: http.Header{
						consts.NamespaceHeaderName: []string{"ns1/"},
					},
				},
			},
			"ns1/",
			"/v1/sys/mounts",
		},
		{
			"non_revocation_relative_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/ns2/sys/mounts",
					},
					Header: http.Header{
						consts.NamespaceHeaderName: []string{"ns1/"},
					},
				},
			},
			"ns1/",
			"/v1/ns2/sys/mounts",
		},
		{
			"revocation_full_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/ns1/sys/leases/revoke",
					},
				},
			},
			"ns1/",
			"/v1/sys/leases/revoke",
		},
		{
			"revocation_relative_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/sys/leases/revoke",
					},
					Header: http.Header{
						consts.NamespaceHeaderName: []string{"ns1/"},
					},
				},
			},
			"ns1/",
			"/v1/sys/leases/revoke",
		},
		{
			"revocation_relative_partial_ns",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/ns2/sys/leases/revoke",
					},
					Header: http.Header{
						consts.NamespaceHeaderName: []string{"ns1/"},
					},
				},
			},
			"ns1/ns2/",
			"/v1/sys/leases/revoke",
		},
		{
			"revocation_prefix_full_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/ns1/sys/leases/revoke-prefix/foo",
					},
				},
			},
			"ns1/",
			"/v1/sys/leases/revoke-prefix/foo",
		},
		{
			"revocation_prefix_relative_path",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/sys/leases/revoke-prefix/foo",
					},
					Header: http.Header{
						consts.NamespaceHeaderName: []string{"ns1/"},
					},
				},
			},
			"ns1/",
			"/v1/sys/leases/revoke-prefix/foo",
		},
		{
			"revocation_prefix_partial_ns",
			&SendRequest{
				Request: &http.Request{
					URL: &url.URL{
						Path: "/v1/ns2/sys/leases/revoke-prefix/foo",
					},
					Header: http.Header{
						consts.NamespaceHeaderName: []string{"ns1/"},
					},
				},
			},
			"ns1/ns2/",
			"/v1/sys/leases/revoke-prefix/foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNamespace, gotRelativePath := deriveNamespaceAndRevocationPath(tt.req)
			if gotNamespace != tt.wantNamespace {
				t.Errorf("deriveNamespaceAndRevocationPath() gotNamespace = %v, want %v", gotNamespace, tt.wantNamespace)
			}
			if gotRelativePath != tt.wantRelativePath {
				t.Errorf("deriveNamespaceAndRevocationPath() gotRelativePath = %v, want %v", gotRelativePath, tt.wantRelativePath)
			}
		})
	}
}

func TestCache_SerializeDeserialize(t *testing.T) {
	// ----- setup cache -----
	// Emulate 2 responses from the api proxy. One returns a new token and the
	// other returns a lease.
	responses := []*SendResponse{
		newTestSendResponse(http.StatusCreated, `{"auth": {"client_token": "testtoken", "renewable": true}}`),
		newTestSendResponse(http.StatusOK, `{"lease_id": "foo", "renewable": true, "data": {"value": "foo"}}`),
	}

	lc := testNewLeaseCache(t, responses, nil)
	// Register a token so that the token and lease requests are cached
	lc.RegisterAutoAuthToken("autoauthtoken")

	// Make a request. A response with a new token is returned to the lease
	// cache and that will be cached.
	urlPath := "http://example.com/v1/sample/api"
	sendReq := &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err := lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[0].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Modify the request a little bit to ensure the second response is
	// returned to the lease cache.
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input_changed"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[1].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}
	// ---------  --------

	testGet, err := lc.db.GetByPrefix(cachememdb.IndexNameID)
	assert.NoError(t, err)
	q.Q("in the test", testGet) // DEBUG

	cereal, err := lc.Serialize()
	assert.NoError(t, err)
	assert.True(t, len(cereal) > 0)

	restoredLC := testNewLeaseCache(t, nil, nil)
	err = restoredLC.DeSerialize(cereal)
	assert.NoError(t, err)

	// Now compare before and after
	beforeDB, err := lc.db.GetByPrefix(cachememdb.IndexNameID)
	assert.NoError(t, err)
	assert.Len(t, beforeDB, 3)
	for _, cachedItem := range beforeDB {
		restoredItem, err := restoredLC.db.Get(cachememdb.IndexNameID, cachedItem.ID)
		assert.NoError(t, err)
		assert.Equal(t, cachedItem.ID, restoredItem.ID)
		assert.Equal(t, cachedItem.Lease, restoredItem.Lease)
		assert.Equal(t, cachedItem.LeaseToken, restoredItem.LeaseToken)
		assert.Equal(t, cachedItem.Namespace, restoredItem.Namespace)
		assert.Equal(t, cachedItem.RequestHeader, restoredItem.RequestHeader)
		assert.Equal(t, cachedItem.RequestMethod, restoredItem.RequestMethod)
		assert.Equal(t, cachedItem.RequestPath, restoredItem.RequestPath)
		assert.Equal(t, cachedItem.RequestToken, restoredItem.RequestToken)
		assert.Equal(t, cachedItem.Response, restoredItem.Response)
		assert.Equal(t, cachedItem.Token, restoredItem.Token)
		assert.Equal(t, cachedItem.TokenAccessor, restoredItem.TokenAccessor)
		assert.Equal(t, cachedItem.TokenParent, restoredItem.TokenParent)

		assert.NotEmpty(t, restoredItem.RenewCtxInfo.CancelFunc)
		assert.NotZero(t, restoredItem.RenewCtxInfo.DoneCh)
		require.NotEmpty(t, restoredItem.RenewCtxInfo.Ctx)
		assert.Equal(t,
			cachedItem.RenewCtxInfo.Ctx.Value(contextIndexID),
			restoredItem.RenewCtxInfo.Ctx.Value(contextIndexID),
		)
	}
	afterDB, err := restoredLC.db.GetByPrefix(cachememdb.IndexNameID)
	assert.NoError(t, err)
	// assert.ElementsMatch(t, beforeDB, afterDB)
	assert.Len(t, afterDB, 3)

	q.Q("before cache", beforeDB)  // DEBUG
	q.Q("restored cache", afterDB) // DEBUG
}

func TestLeaseCache_CacheAndRestore(t *testing.T) {
	// Emulate 2 responses from the api proxy. One returns a new token and the
	// other returns a lease.
	responses := []*SendResponse{
		newTestSendResponse(http.StatusCreated, `{"auth": {"client_token": "testtoken", "renewable": true}}`),
		newTestSendResponse(http.StatusOK, `{"lease_id": "foo", "renewable": true, "data": {"value": "foo"}}`),
	}

	stor := persistcache.NewMapStorage()
	lc := testNewLeaseCache(t, responses, stor)
	// b, err := persistcache.NewBoltStorage("/tmp/mybolt.db")
	// require.NoError(t, err)
	// require.NotNil(t, b)
	// lc := testNewLeaseCache(t, responses, b)

	// Register an token so that the token and lease requests are cached
	lc.RegisterAutoAuthToken("autoauthtoken")

	// Make a request. A response with a new token is returned to the lease
	// cache and that will be cached.
	urlPath := "http://example.com/v1/sample/api"
	sendReq := &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err := lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[0].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Send the same request again to get the cached response
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[0].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Modify the request a little bit to ensure the second response is
	// returned to the lease cache.
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input_changed"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[1].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Make the same request again and ensure that the same response is returned
	// again.
	sendReq = &SendRequest{
		Token:   "autoauthtoken",
		Request: httptest.NewRequest("GET", urlPath, strings.NewReader(`{"value": "input_changed"}`)),
	}
	resp, err = lc.Send(context.Background(), sendReq)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(resp.Response.StatusCode, responses[1].Response.StatusCode); diff != nil {
		t.Fatalf("expected getting proxied response: got %v", diff)
	}

	// Now we know the cache is working, so try restoring from the persisted cache
	q.Q(stor) // DEBUG
	// q.Q(b) // DEBUG - this segfault's

	restoredCache := testNewLeaseCache(t, nil, nil)

	err = restoredCache.Restore(stor)
	// err = restoredCache.Restore(b)
	assert.NoError(t, err)

	// Now compare before and after
	beforeDB, err := lc.db.GetByPrefix(cachememdb.IndexNameID)
	assert.NoError(t, err)
	assert.Len(t, beforeDB, 3)
	for _, cachedItem := range beforeDB {
		restoredItem, err := restoredCache.db.Get(cachememdb.IndexNameID, cachedItem.ID)
		assert.NoError(t, err)
		assert.Equal(t, cachedItem.ID, restoredItem.ID)
		assert.Equal(t, cachedItem.Lease, restoredItem.Lease)
		assert.Equal(t, cachedItem.LeaseToken, restoredItem.LeaseToken)
		assert.Equal(t, cachedItem.Namespace, restoredItem.Namespace)
		assert.Equal(t, cachedItem.RequestHeader, restoredItem.RequestHeader)
		assert.Equal(t, cachedItem.RequestMethod, restoredItem.RequestMethod)
		assert.Equal(t, cachedItem.RequestPath, restoredItem.RequestPath)
		assert.Equal(t, cachedItem.RequestToken, restoredItem.RequestToken)
		assert.Equal(t, cachedItem.Response, restoredItem.Response)
		assert.Equal(t, cachedItem.Token, restoredItem.Token)
		assert.Equal(t, cachedItem.TokenAccessor, restoredItem.TokenAccessor)
		assert.Equal(t, cachedItem.TokenParent, restoredItem.TokenParent)

		assert.NotEmpty(t, restoredItem.RenewCtxInfo.CancelFunc)
		assert.NotZero(t, restoredItem.RenewCtxInfo.DoneCh)
		require.NotEmpty(t, restoredItem.RenewCtxInfo.Ctx)
		assert.Equal(t,
			cachedItem.RenewCtxInfo.Ctx.Value(contextIndexID),
			restoredItem.RenewCtxInfo.Ctx.Value(contextIndexID),
		)
	}
	afterDB, err := restoredCache.db.GetByPrefix(cachememdb.IndexNameID)
	assert.NoError(t, err)
	// assert.ElementsMatch(t, beforeDB, afterDB)
	assert.Len(t, afterDB, 3)

}
