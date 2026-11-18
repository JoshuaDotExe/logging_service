package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"logging-service/internal/logkeymanager"

	"github.com/google/uuid"
)

func TestAccountOwnership(t *testing.T) {
	other := uuid.NewString()
	for _, tc := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{"POST", "/logkey", fmt.Sprintf(`{"account_id":%q}`, other), 403},
		{"GET", "/logkey/account/" + other, "", 403},
		{"GET", "/logkey/" + uuid.NewString(), "", 404},
		{"DELETE", "/logkey/" + uuid.NewString(), "", 404},
	} {
		store := &storeStub{key: logkeymanager.LogKey{AccountID: other}}
		response := serveRequest(testRouter(store, testManagementKey), tc.method, tc.path, tc.body, testManagementKey)
		if response.Code != tc.status {
			t.Fatalf("%s %s status = %d: %s", tc.method, tc.path, response.Code, response.Body.String())
		}
		if tc.method == http.MethodDelete && store.calls != 1 {
			t.Fatal("foreign key deletion reached the write operation")
		}
		if tc.method == http.MethodPost && store.calls != 0 {
			t.Fatal("foreign account creation reached the store")
		}
	}
}
