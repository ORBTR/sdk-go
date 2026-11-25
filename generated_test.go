package orbtr

// types_gen.go and resources_gen.go are what contract/orbtr-v1.json generates: a change to the contract
// lands with its regenerated files, and a hand edit of a generated file fails here.

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ORBTR/sdk-go/internal/gen"
)

func TestGeneratedFilesMatchTheContract(t *testing.T) {
	data, err := os.ReadFile("contract/orbtr-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	files, err := gen.Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range gen.SortedNames(files) {
		current, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(current, files[name]) {
			t.Errorf("%s is not what contract/orbtr-v1.json generates; run go generate ./...", name)
		}
	}
}

func TestEveryContractOperationIsAMethod(t *testing.T) {
	data, err := os.ReadFile("contract/orbtr-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Paths map[string]map[string]struct {
			Resource string          `json:"x-orbtr-resource"`
			Method   string          `json:"x-orbtr-method"`
			Stream   json.RawMessage `json:"x-orbtr-stream"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(WithAPIKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{"create": "Create", "countTokens": "CountTokens", "list": "List", "retrieve": "Get", "cancel": "Cancel", "results": "Results"}
	for _, item := range contract.Paths {
		for _, op := range item {
			v := reflect.ValueOf(c).Elem()
			for _, seg := range splitResource(op.Resource) {
				v = v.FieldByName(gen.GoName(seg))
				if !v.IsValid() {
					t.Fatalf("no service for %s", op.Resource)
				}
				v = v.Elem()
			}
			methods := []string{names[op.Method]}
			if len(op.Stream) > 0 {
				methods = append(methods, names[op.Method]+"Streaming")
			}
			for _, m := range methods {
				if !v.Addr().MethodByName(m).IsValid() {
					t.Errorf("%s has no method %s", op.Resource, m)
				}
			}
		}
	}
}

func splitResource(r string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(r); i++ {
		if i == len(r) || r[i] == '.' {
			out = append(out, r[start:i])
			start = i + 1
		}
	}
	return out
}
