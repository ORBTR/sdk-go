package orbtr

// The licence's use grant names ORBTR's Terms of Service by its address; the module zip carries LICENSE from
// the module root.

import (
	"os"
	"regexp"
	"testing"
)

func TestTheUseGrantLinksTheTerms(t *testing.T) {
	data, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`USE GRANT\.[\s\S]*Terms of Service \(https://orbtr\.ai/terms\)`).Match(data) {
		t.Fatal("LICENSE's use grant does not link https://orbtr.ai/terms")
	}
}
