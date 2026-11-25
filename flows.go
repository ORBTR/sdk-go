package orbtr

import (
	"fmt"
	"strconv"
	"strings"
)

// Flows as models: a published flow is called by naming its id as a call's model, and <flow id>@<version>
// pins a published version. api.orbtr.ai reads a model that starts with "partition:" and holds ":flow:" as
// a flow; any other model is a model id.

// IsFlowModel reports whether a model names a flow.
func IsFlowModel(model string) bool {
	return strings.HasPrefix(model, "partition:") && strings.Contains(model, ":flow:")
}

// FlowModel is the model that calls a published flow: its newest published version when version is 0, or
// the version given.
func FlowModel(flowID string, version int) (string, error) {
	if !IsFlowModel(flowID) || strings.Contains(flowID, "@") {
		return "", fmt.Errorf("orbtr: not a flow id: %q (a flow id looks like partition:<partition>:flow:<name>)", flowID)
	}
	if version < 0 {
		return "", fmt.Errorf("orbtr: a flow version is a whole number from 1, not %d", version)
	}
	if version == 0 {
		return flowID, nil
	}
	return flowID + "@" + strconv.Itoa(version), nil
}

// MustFlowModel is FlowModel for a flow id and version known to be valid; it panics otherwise.
func MustFlowModel(flowID string, version int) string {
	m, err := FlowModel(flowID, version)
	if err != nil {
		panic(err)
	}
	return m
}

// ParseFlowModel splits a flow model into its flow id and pinned version (0 for the newest); ok is false
// for a model that names no flow.
func ParseFlowModel(model string) (flowID string, version int, ok bool) {
	if !IsFlowModel(model) {
		return "", 0, false
	}
	i := strings.LastIndex(model, "@")
	if i < 0 {
		return model, 0, true
	}
	n, err := strconv.Atoi(model[i+1:])
	if err != nil || n < 1 {
		return model[:i], 0, true
	}
	return model[:i], n, true
}
