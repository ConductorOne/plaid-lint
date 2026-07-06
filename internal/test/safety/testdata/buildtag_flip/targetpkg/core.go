package targetpkg

// Core is always present; the 3 consumers reference it.

type Core struct {
	Name string
}

func NewCore(name string) *Core { return &Core{Name: name} }
