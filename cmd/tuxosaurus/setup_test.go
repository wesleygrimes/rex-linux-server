package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wesleygrimes/tuxosaurus/internal/tailnet"
)

func TestEnrollmentProgress(t *testing.T) {
	states := []tailnet.Status{
		{State: "needs_login", AuthURL: "https://login.tailscale.com/a/test"},
		{State: "needs_login", AuthURL: "https://login.tailscale.com/a/test"},
		{State: "needs_machine_approval"},
		{State: "running", HTTPS: &tailnet.HTTPSStatus{State: "available", URL: "https://tuxosaurus.tail.test"}},
	}
	i := 0
	var out bytes.Buffer
	url, err := waitForEnrollment(context.Background(), func() tailnet.Status {
		s := states[i]
		if i < len(states)-1 {
			i++
		}
		return s
	}, &out)
	if err != nil || url != "https://tuxosaurus.tail.test" {
		t.Fatalf("URL=%q error=%v", url, err)
	}
	if strings.Count(out.String(), "https://login.tailscale.com/a/test") != 1 || !strings.Contains(out.String(), "administrator") {
		t.Fatalf("incorrect enrollment progress: %s", &out)
	}
}

func TestEnrollmentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForEnrollment(ctx, func() tailnet.Status { return tailnet.Status{State: "starting"} }, &bytes.Buffer{}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
