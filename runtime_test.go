package main

import (
	"context"
	"strings"
	"testing"
)

func TestAskOwnsProviderSupportAndAuthentication(t *testing.T) {
	a, _, _ := newTestApp(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	for _, model := range []string{"anthropic/gateway-model", "new-provider/new-model"} {
		if report := a.modelReadinessFor(model); report.State != "unproved" {
			t.Fatalf("guessed Ask authentication/support: %+v", report)
		}
	}
	a.tools.paths["ask"] = writeScript(t, t.TempDir(), "ask", "#!/bin/sh\nprintf 'ask: provider does not support structured output\\n' >&2\nexit 1\n")
	proof, err := a.proveModel(context.Background(), "new-provider/new-model")
	if err != nil || proof.OK || !strings.Contains(proof.Output, "provider does not support") {
		t.Fatalf("Ask refusal: %+v %v", proof, err)
	}
	if report := a.modelReadinessFor(proof.Model); report.State != "unproved" || !strings.Contains(report.Message, "last test call failed") {
		t.Fatalf("provider error was hidden: %+v", report)
	}
}

func TestModelProofRequiresExactReply(t *testing.T) {
	a, _, _ := newTestApp(t)
	for _, reply := range []string{"not okay", "token expired", "OK"} {
		t.Setenv("FAKE_ASK_REPLY", writeTestReply(t, t.TempDir(), "reply.txt", reply))
		proof, err := a.proveModel(context.Background(), "openai/test")
		if err != nil || proof.OK != (reply == "OK") {
			t.Fatalf("reply %q proof=%+v err=%v", reply, proof, err)
		}
	}
}
