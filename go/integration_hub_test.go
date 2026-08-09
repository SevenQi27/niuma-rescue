package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type integrationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f integrationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestIntegrationHubSaveKeepsSecrets(t *testing.T) {
	dir := t.TempDir()
	hub := newIntegrationHub(dir)
	first, err := hub.save("zentao", connectorSettings{
		Name: "测试禅道", BaseURL: "http://zentao.local/", APIPath: "/api.php/v1",
		APIToken: "token-one", WebhookSecret: "hook-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Configured || !first.HasAPIToken || !first.HasWebhookSecret || first.BaseURL != "http://zentao.local" {
		t.Fatalf("unexpected first status: %#v", first)
	}
	second, err := hub.save("zentao", connectorSettings{
		Name: "新的名称", BaseURL: "http://zentao.local", APIPath: "/api.php/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.HasAPIToken || !second.HasWebhookSecret {
		t.Fatalf("blank secret fields must preserve saved values: %#v", second)
	}
	info, err := os.Stat(filepath.Join(dir, "integrations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%o, want 600", info.Mode().Perm())
	}
}

func TestIntegrationHubJiraConnection(t *testing.T) {
	hub := newIntegrationHub(t.TempDir())
	hub.client = &http.Client{Transport: integrationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		user, token, ok := request.BasicAuth()
		if request.URL.Path != "/rest/api/3/myself" || !ok || user != "dev@example.com" || token != "jira-token" {
			t.Fatalf("unexpected request: url=%s user=%s token=%s", request.URL.String(), user, token)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accountId":"tester"}`))}, nil
	})}
	status, err := hub.test(t.Context(), "jira", connectorSettings{
		BaseURL: "http://jira.local", Deployment: "cloud", APIPath: "/rest/api/3",
		APIUser: "dev@example.com", APIToken: "jira-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.LastTestAt == "" || status.LastMessage == "" || status.LastTestError != "" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestIntegrationWebhookDeduplicatesEvents(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	hub := newIntegrationHub(dir)
	if _, err := hub.save("zentao", connectorSettings{
		Enabled: true, BaseURL: "http://zentao.local", APIToken: "api-token",
		WebhookSecret: "webhook-token", APIPath: "/api.php/v1",
	}); err != nil {
		t.Fatal(err)
	}
	console := &bugConsole{fs: &fakeBugStore{}, stateDir: dir, app: &App{st: store, integrations: hub}}
	handler := console.handler()
	body := `{"objectType":"bug","objectID":"42","action":"opened","date":"2026-08-09 17:00:00"}`

	first := webRequest(handler, http.MethodPost, "/api/integrations/zentao/events?token=webhook-token", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := webRequest(handler, http.MethodPost, "/api/integrations/zentao/events?token=webhook-token", body)
	if second.Code != http.StatusAccepted {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	var response struct {
		Inserted bool `json:"inserted"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Inserted {
		t.Fatal("duplicate webhook must not insert a second event")
	}
	if events := store.listIntegrationEvents(10); len(events) != 1 || events[0].IntegrationKind != "zentao" {
		t.Fatalf("unexpected events: %#v", events)
	}

	unauthorized := webRequest(handler, http.MethodPost, "/api/integrations/zentao/events?token=wrong", body)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
}

func TestJiraEventKeyDoesNotCollapseDifferentIssues(t *testing.T) {
	first := []byte(`{"webhookEvent":"jira:issue_created","issue":{"id":"10001"},"timestamp":1}`)
	second := []byte(`{"webhookEvent":"jira:issue_created","issue":{"id":"10002"},"timestamp":2}`)
	if integrationEventKey("jira", first) == integrationEventKey("jira", second) {
		t.Fatal("different Jira issues must have different idempotency keys")
	}
	if integrationEventKey("jira", first) != integrationEventKey("jira", first) {
		t.Fatal("the same Jira payload must keep a stable idempotency key")
	}
}
