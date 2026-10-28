package engines

import (
	"fmt"
	"slices"
	"testing"
)

func TestStepsAreTheLinesCorePrintsHoweverTheyAreWritten(t *testing.T) {
	var got []string
	w := &steps{progress: func(s string) { got = append(got, s) }}
	fmt.Fprint(w, "create network web-a\ncreate vol")
	fmt.Fprint(w, "ume web-a-home\n\n  \n")
	fmt.Fprintln(w, "create container web-a-1")
	if want := []string{"create network web-a", "create volume web-a-home", "create container web-a-1"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
}
