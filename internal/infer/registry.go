package infer

var registered []Inferrer

// register adds an inferrer to the ordered registry. Called from each
// inferrer file's init.
func register(in Inferrer) { registered = append(registered, in) }
