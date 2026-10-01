package app

import "testing"

func TestWorkerCatalogMarksNativeReady(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	found := false
	for _, info := range application.WorkerCatalog() {
		if info.Kind != "termixgo" {
			continue
		}
		found = true
		if !info.Available {
			t.Errorf("the native worker should always be ready, got %+v", info)
		}
	}
	if !found {
		t.Errorf("the catalog should include the native worker")
	}
}
