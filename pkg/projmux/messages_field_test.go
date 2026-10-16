package projmux_test

import (
	"bytes"
	"encoding/json"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"reflect"
	"testing"
)

func TestPeerMessagePublicFieldCoverage(t *testing.T) {
	for _, pair := range [][2]reflect.Type{{reflect.TypeFor[agentmessages.Receipt](), reflect.TypeFor[core.MessageReceipt]()}, {reflect.TypeFor[message.Route](), reflect.TypeFor[core.MessageRoute]()}, {reflect.TypeFor[message.Delivery](), reflect.TypeFor[core.MessageDelivery]()}} {
		a, b := reflect.New(pair[0]).Elem(), reflect.New(pair[1]).Elem()
		fillResourceFields(a)
		fillResourceFields(b)
		x, _ := json.Marshal(a.Interface())
		y, _ := json.Marshal(b.Interface())
		if !bytes.Equal(x, y) {
			t.Fatalf("field graph differs %s %s", x, y)
		}
	}
}
