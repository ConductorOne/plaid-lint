module example.com/plaidexample

go 1.26

// A local replace without an allow-list entry: gomoddirectives finding.
replace example.com/other => ./other
