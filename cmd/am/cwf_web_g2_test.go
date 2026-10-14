package main

import (
	"strings"
	"testing"
)

// FIND-0371 (repair): the label and meta adds clear their inputs programmatically,
// which fires no input/change event, so they must run the refresh that the SSE
// echo deferred while the inputs still held text.
func TestCwfWebModalAddsRunDeferredRefresh(t *testing.T) {
	r := cwfWebFunc(t, cwfWebAppJS(t), "renderModal")
	for _, c := range []struct{ start, end, clear string }{
		{"labelBox.onkeydown", "labelsDiv.append(labelBox)", `labelBox.value = "";`},
		{"const addMeta", "const onMetaKey", `valBox.value = "";`},
	} {
		i := strings.Index(r, c.start)
		j := strings.Index(r, c.end)
		if i < 0 || j < i {
			t.Fatalf("renderModal: handler %q not found", c.start)
		}
		cwfWebBefore(t, c.start, r[i:j], c.clear, "runDeferredRefresh();")
	}
}
