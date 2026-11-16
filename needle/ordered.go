package needle

// object is a JSON object that remembers the order its keys were inserted
// in. The engine renders the tool schemas into the model's prompt verbatim,
// so the serialized byte order has to match the reference serializer
// (Python json.dumps keeps insertion order); plain Go maps cannot.
//
// It is used while a schema is being built; Schema and the other public
// surfaces still expose plain map[string]any views built from it.
type object struct {
	keys []string
	vals map[string]any
}

func newObj() *object {
	return &object{vals: map[string]any{}}
}

// set inserts a key (or overwrites an existing one in place), so the
// insertion order of a rebuilt key is stable.
func (o *object) set(key string, value any) *object {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
	return o
}

// get returns the value stored under key.
func (o *object) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// has reports whether key is present.
func (o *object) has(key string) bool {
	_, ok := o.vals[key]
	return ok
}

// del removes a key, preserving the order of the rest.
func (o *object) del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// len reports the number of keys.
func (o *object) len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// view converts an ordered object into the plain map the public API and the
// grounding passes work with. Values are converted recursively; anything
// that is not an object is returned unchanged.
func (o *object) view() map[string]any {
	if o == nil {
		return nil
	}
	out := make(map[string]any, len(o.keys))
	for _, k := range o.keys {
		out[k] = toPlain(o.vals[k])
	}
	return out
}

// toPlain converts every ordered object inside a value (schemas, property
// trees, slices) into plain maps, leaving other values untouched.
func toPlain(v any) any {
	switch t := v.(type) {
	case *object:
		return t.view()
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = toPlain(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = toPlain(val)
		}
		return out
	}
	return v
}
