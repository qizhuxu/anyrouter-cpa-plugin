package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDebugChallengeEval is a scratch probe used while tuning the interpreter
// limits. It is skipped in normal runs.
func TestDebugChallengeEval(t *testing.T) {
	if os.Getenv("ANYROUTER_DEBUG_CHALLENGE") == "" {
		t.Skip("set ANYROUTER_DEBUG_CHALLENGE=1 to run")
	}
	body, errRead := os.ReadFile(filepath.Clean(fixturePath))
	if errRead != nil {
		t.Fatalf("read fixture: %v", errRead)
	}
	scripts := extractScripts(string(body))
	t.Logf("scripts found: %d", len(scripts))
	for _, limit := range []int{256, 512, 1024, 2048, 8192, 0} {
		for index, script := range scripts {
			start := time.Now()
			cookies, errRun := runScriptCollectCookiesLimit(script, defaultUserAgent, 20*time.Second, limit)
			t.Logf("limit=%d script=%d elapsed=%s cookies=%v err=%#v", limit, index, time.Since(start), cookies, errRun)
			if errRun != nil {
				t.Logf("   error detail: %s", fmt.Sprintf("%v", errRun))
			}
		}
	}
}
