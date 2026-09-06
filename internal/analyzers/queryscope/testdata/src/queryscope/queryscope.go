package queryscope

type Context struct{}

type Select struct{}
type SelectDataset struct{}
type Fields struct{}

func (Fields) TenantId() string                         { return "tenant" }
func (*Select) CountStar(...any) *Select                { return nil }
func (*Select) SelectCol(...any) *Select                { return nil }
func (*Select) Select(...any) *Select                   { return nil }
func (*Select) Count(...any) *Select                    { return nil }
func (*Select) Where(...any) *Select                    { return nil }
func (*Select) With(...any) *Select                     { return nil }
func (*Select) RawWith(...any) *Select                  { return nil }
func (*Select) ScopeJoinedTable(...any) *Select         { return nil }
func (*Select) WithDangerousCrossTenant(...any) *Select { return nil }
func (*Select) IgnoreTenantCheck(...any) *Select        { return nil }
func (*Select) From(...any) *Select                     { return nil }

var fields Fields
var q = &Select{}

func missingScope() {
	q.CountStar("count").From("items") // want "queryscope: selector requires an explicit configured scope"
}

func modelCarryingSelector() {
	q.CountStar("count").SelectCol("id")
}

func modelAliasSelector() {
	q.CountStar("count").Select("model", "items")
}

func unrelatedSelectShape() {
	q.CountStar("count").Select("model") // want "queryscope: selector requires an explicit configured scope"
}

func explicitScopePredicate() {
	q.CountStar("count").Where(fields.TenantId())
}

func rawScopePredicate() {
	q.CountStar("count").Where("tenant_id = ?")
}

func joinedTableScope() {
	q.CountStar("count").ScopeJoinedTable()
}

func queryWrapper(incoming *Select) {
	q.CountStar("count")
	_ = incoming
}

func queryDatasetWrapper(incoming *SelectDataset) {
	q.CountStar("count")
	_ = incoming
}

// queryscope:ignore reporting query intentionally crosses scopes
func documentedOptOut() {
	q.CountStar("count")
}

// queryscope:ignore
func undocumentedOptOut() {
	q.CountStar("count") // want "queryscope: selector requires an explicit configured scope"
}

func methodOptOut() {
	q.CountStar("count").WithDangerousCrossTenant()
}

func containsOptOut() {
	q.CountStar("count").IgnoreTenantCheck()
}

func cteScope() {
	q.CountStar("count").With("alias", false, false)
}

func unrelatedWith() {
	q.CountStar("count").With("alias") // want "queryscope: selector requires an explicit configured scope"
}

var packageLiteral = func() {
	q.CountStar("count") // want "queryscope: selector requires an explicit configured scope"
}
