package good

import "time"

type Result struct {
	Modified time.Time
	Values   map[string][]byte
}

func Read() Result { return Result{} }
