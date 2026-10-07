package cluster

import (
	"encoding/json"

	"gpushare-elastic/pkg/profile"
)

func marshalDoc(doc profile.Doc) ([]byte, error) {
	return json.Marshal(doc)
}
