// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"errors"
	"fmt"
)

// LintersSettings holds the per-linter configuration blocks. Field
// shape mirrors upstream's `LintersSettings` struct verbatim (see
// SCHEMA.md). Every linter named here has its config knobs typed; for
// linters added upstream after this snapshot, the Extra catch-all
// preserves the raw YAML so the parser does not lose data.
type LintersSettings struct {
	Asasalint                AsasalintSettings                `yaml:"asasalint,omitempty" json:"asasalint,omitempty"`
	BiDiChk                  BiDiChkSettings                  `yaml:"bidichk,omitempty" json:"bidichk,omitempty"`
	CopyLoopVar              CopyLoopVarSettings              `yaml:"copyloopvar,omitempty" json:"copyloopvar,omitempty"`
	Cyclop                   CyclopSettings                   `yaml:"cyclop,omitempty" json:"cyclop,omitempty"`
	Decorder                 DecorderSettings                 `yaml:"decorder,omitempty" json:"decorder,omitempty"`
	Depguard                 DepGuardSettings                 `yaml:"depguard,omitempty" json:"depguard,omitempty"`
	Dogsled                  DogsledSettings                  `yaml:"dogsled,omitempty" json:"dogsled,omitempty"`
	Dupl                     DuplSettings                     `yaml:"dupl,omitempty" json:"dupl,omitempty"`
	DupWord                  DupWordSettings                  `yaml:"dupword,omitempty" json:"dupword,omitempty"`
	EmbeddedStructFieldCheck EmbeddedStructFieldCheckSettings `yaml:"embeddedstructfieldcheck,omitempty" json:"embeddedstructfieldcheck,omitempty"`
	Errcheck                 ErrcheckSettings                 `yaml:"errcheck,omitempty" json:"errcheck,omitempty"`
	ErrChkJSON               ErrChkJSONSettings               `yaml:"errchkjson,omitempty" json:"errchkjson,omitempty"`
	ErrorLint                ErrorLintSettings                `yaml:"errorlint,omitempty" json:"errorlint,omitempty"`
	Exhaustive               ExhaustiveSettings               `yaml:"exhaustive,omitempty" json:"exhaustive,omitempty"`
	Exhaustruct              ExhaustructSettings              `yaml:"exhaustruct,omitempty" json:"exhaustruct,omitempty"`
	Fatcontext               FatcontextSettings               `yaml:"fatcontext,omitempty" json:"fatcontext,omitempty"`
	Forbidigo                ForbidigoSettings                `yaml:"forbidigo,omitempty" json:"forbidigo,omitempty"`
	FuncOrder                FuncOrderSettings                `yaml:"funcorder,omitempty" json:"funcorder,omitempty"`
	Funlen                   FunlenSettings                   `yaml:"funlen,omitempty" json:"funlen,omitempty"`
	GinkgoLinter             GinkgoLinterSettings             `yaml:"ginkgolinter,omitempty" json:"ginkgolinter,omitempty"`
	Gocognit                 GocognitSettings                 `yaml:"gocognit,omitempty" json:"gocognit,omitempty"`
	GoChecksumType           GoChecksumTypeSettings           `yaml:"gochecksumtype,omitempty" json:"gochecksumtype,omitempty"`
	Goconst                  GoConstSettings                  `yaml:"goconst,omitempty" json:"goconst,omitempty"`
	Gocritic                 GoCriticSettings                 `yaml:"gocritic,omitempty" json:"gocritic,omitempty"`
	Gocyclo                  GoCycloSettings                  `yaml:"gocyclo,omitempty" json:"gocyclo,omitempty"`
	Godoclint                GodoclintSettings                `yaml:"godoclint,omitempty" json:"godoclint,omitempty"`
	Godot                    GodotSettings                    `yaml:"godot,omitempty" json:"godot,omitempty"`
	Godox                    GodoxSettings                    `yaml:"godox,omitempty" json:"godox,omitempty"`
	Goheader                 GoHeaderSettings                 `yaml:"goheader,omitempty" json:"goheader,omitempty"`
	GoModDirectives          GoModDirectivesSettings          `yaml:"gomoddirectives,omitempty" json:"gomoddirectives,omitempty"`
	Gomodguard               GoModGuardSettings               `yaml:"gomodguard,omitempty" json:"gomodguard,omitempty"`
	Gosec                    GoSecSettings                    `yaml:"gosec,omitempty" json:"gosec,omitempty"`
	Gosmopolitan             GosmopolitanSettings             `yaml:"gosmopolitan,omitempty" json:"gosmopolitan,omitempty"`
	Unqueryvet               UnqueryvetSettings               `yaml:"unqueryvet,omitempty" json:"unqueryvet,omitempty"`
	Govet                    GovetSettings                    `yaml:"govet,omitempty" json:"govet,omitempty"`
	Grouper                  GrouperSettings                  `yaml:"grouper,omitempty" json:"grouper,omitempty"`
	Iface                    IfaceSettings                    `yaml:"iface,omitempty" json:"iface,omitempty"`
	ImportAs                 ImportAsSettings                 `yaml:"importas,omitempty" json:"importas,omitempty"`
	Inamedparam              INamedParamSettings              `yaml:"inamedparam,omitempty" json:"inamedparam,omitempty"`
	Ineffassign              IneffassignSettings              `yaml:"ineffassign,omitempty" json:"ineffassign,omitempty"`
	InterfaceBloat           InterfaceBloatSettings           `yaml:"interfacebloat,omitempty" json:"interfacebloat,omitempty"`
	IotaMixing               IotaMixingSettings               `yaml:"iotamixing,omitempty" json:"iotamixing,omitempty"`
	Ireturn                  IreturnSettings                  `yaml:"ireturn,omitempty" json:"ireturn,omitempty"`
	Lll                      LllSettings                      `yaml:"lll,omitempty" json:"lll,omitempty"`
	LoggerCheck              LoggerCheckSettings              `yaml:"loggercheck,omitempty" json:"loggercheck,omitempty"`
	MaintIdx                 MaintIdxSettings                 `yaml:"maintidx,omitempty" json:"maintidx,omitempty"`
	Makezero                 MakezeroSettings                 `yaml:"makezero,omitempty" json:"makezero,omitempty"`
	Misspell                 MisspellSettings                 `yaml:"misspell,omitempty" json:"misspell,omitempty"`
	Mnd                      MndSettings                      `yaml:"mnd,omitempty" json:"mnd,omitempty"`
	Modernize                ModernizeSettings                `yaml:"modernize,omitempty" json:"modernize,omitempty"`
	MustTag                  MustTagSettings                  `yaml:"musttag,omitempty" json:"musttag,omitempty"`
	Nakedret                 NakedretSettings                 `yaml:"nakedret,omitempty" json:"nakedret,omitempty"`
	Nestif                   NestifSettings                   `yaml:"nestif,omitempty" json:"nestif,omitempty"`
	NilNil                   NilNilSettings                   `yaml:"nilnil,omitempty" json:"nilnil,omitempty"`
	Nlreturn                 NlreturnSettings                 `yaml:"nlreturn,omitempty" json:"nlreturn,omitempty"`
	NoLintLint               NoLintLintSettings               `yaml:"nolintlint,omitempty" json:"nolintlint,omitempty"`
	NoNamedReturns           NoNamedReturnsSettings           `yaml:"nonamedreturns,omitempty" json:"nonamedreturns,omitempty"`
	ParallelTest             ParallelTestSettings             `yaml:"paralleltest,omitempty" json:"paralleltest,omitempty"`
	PerfSprint               PerfSprintSettings               `yaml:"perfsprint,omitempty" json:"perfsprint,omitempty"`
	Prealloc                 PreallocSettings                 `yaml:"prealloc,omitempty" json:"prealloc,omitempty"`
	Predeclared              PredeclaredSettings              `yaml:"predeclared,omitempty" json:"predeclared,omitempty"`
	Promlinter               PromlinterSettings               `yaml:"promlinter,omitempty" json:"promlinter,omitempty"`
	ProtoGetter              ProtoGetterSettings              `yaml:"protogetter,omitempty" json:"protogetter,omitempty"`
	Reassign                 ReassignSettings                 `yaml:"reassign,omitempty" json:"reassign,omitempty"`
	Recvcheck                RecvcheckSettings                `yaml:"recvcheck,omitempty" json:"recvcheck,omitempty"`
	Revive                   ReviveSettings                   `yaml:"revive,omitempty" json:"revive,omitempty"`
	RowsErrCheck             RowsErrCheckSettings             `yaml:"rowserrcheck,omitempty" json:"rowserrcheck,omitempty"`
	SlogLint                 SlogLintSettings                 `yaml:"sloglint,omitempty" json:"sloglint,omitempty"`
	Spancheck                SpancheckSettings                `yaml:"spancheck,omitempty" json:"spancheck,omitempty"`
	Staticcheck              StaticCheckSettings              `yaml:"staticcheck,omitempty" json:"staticcheck,omitempty"`
	TagAlign                 TagAlignSettings                 `yaml:"tagalign,omitempty" json:"tagalign,omitempty"`
	Tagliatelle              TagliatelleSettings              `yaml:"tagliatelle,omitempty" json:"tagliatelle,omitempty"`
	Testifylint              TestifylintSettings              `yaml:"testifylint,omitempty" json:"testifylint,omitempty"`
	Testpackage              TestpackageSettings              `yaml:"testpackage,omitempty" json:"testpackage,omitempty"`
	Thelper                  ThelperSettings                  `yaml:"thelper,omitempty" json:"thelper,omitempty"`
	Unconvert                UnconvertSettings                `yaml:"unconvert,omitempty" json:"unconvert,omitempty"`
	Unparam                  UnparamSettings                  `yaml:"unparam,omitempty" json:"unparam,omitempty"`
	Unused                   UnusedSettings                   `yaml:"unused,omitempty" json:"unused,omitempty"`
	UseStdlibVars            UseStdlibVarsSettings            `yaml:"usestdlibvars,omitempty" json:"usestdlibvars,omitempty"`
	UseTesting               UseTestingSettings               `yaml:"usetesting,omitempty" json:"usetesting,omitempty"`
	Varnamelen               VarnamelenSettings               `yaml:"varnamelen,omitempty" json:"varnamelen,omitempty"`
	Whitespace               WhitespaceSettings               `yaml:"whitespace,omitempty" json:"whitespace,omitempty"`
	Wrapcheck                WrapcheckSettings                `yaml:"wrapcheck,omitempty" json:"wrapcheck,omitempty"`
	WSL                      WSLv4Settings                    `yaml:"wsl,omitempty" json:"wsl,omitempty"` // Deprecated: use WSLv5 instead.
	WSLv5                    WSLv5Settings                    `yaml:"wsl_v5,omitempty" json:"wsl_v5,omitempty"`

	Custom map[string]CustomLinterSettings `yaml:"custom,omitempty" json:"custom,omitempty"`
}

// Validate dispatches to per-linter validators that have non-trivial
// rules. Most blocks accept any field combination.
func (s *LintersSettings) Validate() error {
	if err := s.Govet.Validate(); err != nil {
		return err
	}
	for name, settings := range s.Custom {
		if err := settings.Validate(); err != nil {
			return fmt.Errorf("linters.settings.custom[%q]: %w", name, err)
		}
	}
	return nil
}

// AsasalintSettings — `asasalint` linter.
type AsasalintSettings struct {
	Exclude              []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	UseBuiltinExclusions bool     `yaml:"use-builtin-exclusions,omitempty" json:"use-builtin-exclusions,omitempty"`
}

// BiDiChkSettings — `bidichk` linter.
type BiDiChkSettings struct {
	LeftToRightEmbedding     bool `yaml:"left-to-right-embedding,omitempty" json:"left-to-right-embedding,omitempty"`
	RightToLeftEmbedding     bool `yaml:"right-to-left-embedding,omitempty" json:"right-to-left-embedding,omitempty"`
	PopDirectionalFormatting bool `yaml:"pop-directional-formatting,omitempty" json:"pop-directional-formatting,omitempty"`
	LeftToRightOverride      bool `yaml:"left-to-right-override,omitempty" json:"left-to-right-override,omitempty"`
	RightToLeftOverride      bool `yaml:"right-to-left-override,omitempty" json:"right-to-left-override,omitempty"`
	LeftToRightIsolate       bool `yaml:"left-to-right-isolate,omitempty" json:"left-to-right-isolate,omitempty"`
	RightToLeftIsolate       bool `yaml:"right-to-left-isolate,omitempty" json:"right-to-left-isolate,omitempty"`
	FirstStrongIsolate       bool `yaml:"first-strong-isolate,omitempty" json:"first-strong-isolate,omitempty"`
	PopDirectionalIsolate    bool `yaml:"pop-directional-isolate,omitempty" json:"pop-directional-isolate,omitempty"`
}

// CopyLoopVarSettings — `copyloopvar` linter.
type CopyLoopVarSettings struct {
	CheckAlias bool `yaml:"check-alias,omitempty" json:"check-alias,omitempty"`
}

// CyclopSettings — `cyclop` linter.
type CyclopSettings struct {
	MaxComplexity  int     `yaml:"max-complexity,omitempty" json:"max-complexity,omitempty"`
	PackageAverage float64 `yaml:"package-average,omitempty" json:"package-average,omitempty"`
}

// DepGuardSettings — `depguard` linter. `rules` is a map from rule name
// to a list of allow/deny patterns; map keys are stable for canonical
// hashing.
type DepGuardSettings struct {
	Rules map[string]*DepGuardList `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// DepGuardList — one named depguard ruleset.
type DepGuardList struct {
	ListMode string         `yaml:"list-mode,omitempty" json:"list-mode,omitempty"`
	Files    []string       `yaml:"files,omitempty" json:"files,omitempty"`
	Allow    []string       `yaml:"allow,omitempty" json:"allow,omitempty"`
	Deny     []DepGuardDeny `yaml:"deny,omitempty" json:"deny,omitempty"`
}

// DepGuardDeny is one {pkg, desc} pair under depguard.
type DepGuardDeny struct {
	Pkg  string `yaml:"pkg,omitempty" json:"pkg,omitempty"`
	Desc string `yaml:"desc,omitempty" json:"desc,omitempty"`
}

// DecorderSettings — `decorder` linter.
type DecorderSettings struct {
	DecOrder                  []string `yaml:"dec-order,omitempty" json:"dec-order,omitempty"`
	IgnoreUnderscoreVars      bool     `yaml:"ignore-underscore-vars,omitempty" json:"ignore-underscore-vars,omitempty"`
	DisableDecNumCheck        bool     `yaml:"disable-dec-num-check,omitempty" json:"disable-dec-num-check,omitempty"`
	DisableTypeDecNumCheck    bool     `yaml:"disable-type-dec-num-check,omitempty" json:"disable-type-dec-num-check,omitempty"`
	DisableConstDecNumCheck   bool     `yaml:"disable-const-dec-num-check,omitempty" json:"disable-const-dec-num-check,omitempty"`
	DisableVarDecNumCheck     bool     `yaml:"disable-var-dec-num-check,omitempty" json:"disable-var-dec-num-check,omitempty"`
	DisableDecOrderCheck      bool     `yaml:"disable-dec-order-check,omitempty" json:"disable-dec-order-check,omitempty"`
	DisableInitFuncFirstCheck bool     `yaml:"disable-init-func-first-check,omitempty" json:"disable-init-func-first-check,omitempty"`
}

// DogsledSettings — `dogsled` linter.
type DogsledSettings struct {
	MaxBlankIdentifiers int `yaml:"max-blank-identifiers,omitempty" json:"max-blank-identifiers,omitempty"`
}

// DuplSettings — `dupl` linter.
type DuplSettings struct {
	Threshold int `yaml:"threshold,omitempty" json:"threshold,omitempty"`
}

// DupWordSettings — `dupword` linter.
type DupWordSettings struct {
	Keywords     []string `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	Ignore       []string `yaml:"ignore,omitempty" json:"ignore,omitempty"`
	CommentsOnly bool     `yaml:"comments-only,omitempty" json:"comments-only,omitempty"`
}

// EmbeddedStructFieldCheckSettings — `embeddedstructfieldcheck` linter.
type EmbeddedStructFieldCheckSettings struct {
	ForbidMutex bool `yaml:"forbid-mutex,omitempty" json:"forbid-mutex,omitempty"`
	EmptyLine   bool `yaml:"empty-line,omitempty" json:"empty-line,omitempty"`
}

// ErrcheckSettings — `errcheck` linter.
type ErrcheckSettings struct {
	DisableDefaultExclusions bool     `yaml:"disable-default-exclusions,omitempty" json:"disable-default-exclusions,omitempty"`
	CheckTypeAssertions      bool     `yaml:"check-type-assertions,omitempty" json:"check-type-assertions,omitempty"`
	CheckAssignToBlank       bool     `yaml:"check-blank,omitempty" json:"check-blank,omitempty"`
	ExcludeFunctions         []string `yaml:"exclude-functions,omitempty" json:"exclude-functions,omitempty"`
	Verbose                  bool     `yaml:"verbose,omitempty" json:"verbose,omitempty"`
}

// ErrChkJSONSettings — `errchkjson` linter.
type ErrChkJSONSettings struct {
	CheckErrorFreeEncoding bool `yaml:"check-error-free-encoding,omitempty" json:"check-error-free-encoding,omitempty"`
	ReportNoExported       bool `yaml:"report-no-exported,omitempty" json:"report-no-exported,omitempty"`
}

// ErrorLintSettings — `errorlint` linter.
type ErrorLintSettings struct {
	Errorf                bool                 `yaml:"errorf,omitempty" json:"errorf,omitempty"`
	ErrorfMulti           bool                 `yaml:"errorf-multi,omitempty" json:"errorf-multi,omitempty"`
	Asserts               bool                 `yaml:"asserts,omitempty" json:"asserts,omitempty"`
	Comparison            bool                 `yaml:"comparison,omitempty" json:"comparison,omitempty"`
	AllowedErrors         []ErrorLintAllowPair `yaml:"allowed-errors,omitempty" json:"allowed-errors,omitempty"`
	AllowedErrorsWildcard []ErrorLintAllowPair `yaml:"allowed-errors-wildcard,omitempty" json:"allowed-errors-wildcard,omitempty"`
}

// ErrorLintAllowPair — one allowed err/fun pair under errorlint.
type ErrorLintAllowPair struct {
	Err string `yaml:"err,omitempty" json:"err,omitempty"`
	Fun string `yaml:"fun,omitempty" json:"fun,omitempty"`
}

// ExhaustiveSettings — `exhaustive` linter.
type ExhaustiveSettings struct {
	Check                      []string `yaml:"check,omitempty" json:"check,omitempty"`
	DefaultSignifiesExhaustive bool     `yaml:"default-signifies-exhaustive,omitempty" json:"default-signifies-exhaustive,omitempty"`
	IgnoreEnumMembers          string   `yaml:"ignore-enum-members,omitempty" json:"ignore-enum-members,omitempty"`
	IgnoreEnumTypes            string   `yaml:"ignore-enum-types,omitempty" json:"ignore-enum-types,omitempty"`
	PackageScopeOnly           bool     `yaml:"package-scope-only,omitempty" json:"package-scope-only,omitempty"`
	ExplicitExhaustiveMap      bool     `yaml:"explicit-exhaustive-map,omitempty" json:"explicit-exhaustive-map,omitempty"`
	ExplicitExhaustiveSwitch   bool     `yaml:"explicit-exhaustive-switch,omitempty" json:"explicit-exhaustive-switch,omitempty"`
	DefaultCaseRequired        bool     `yaml:"default-case-required,omitempty" json:"default-case-required,omitempty"`
}

// ExhaustructSettings — `exhaustruct` linter.
type ExhaustructSettings struct {
	Include                []string `yaml:"include,omitempty" json:"include,omitempty"`
	Exclude                []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	AllowEmpty             bool     `yaml:"allow-empty,omitempty" json:"allow-empty,omitempty"`
	AllowEmptyRx           []string `yaml:"allow-empty-rx,omitempty" json:"allow-empty-rx,omitempty"`
	AllowEmptyReturns      bool     `yaml:"allow-empty-returns,omitempty" json:"allow-empty-returns,omitempty"`
	AllowEmptyDeclarations bool     `yaml:"allow-empty-declarations,omitempty" json:"allow-empty-declarations,omitempty"`
}

// FatcontextSettings — `fatcontext` linter.
type FatcontextSettings struct {
	CheckStructPointers bool `yaml:"check-struct-pointers,omitempty" json:"check-struct-pointers,omitempty"`
}

// ForbidigoSettings — `forbidigo` linter.
type ForbidigoSettings struct {
	Forbid               []ForbidigoPattern `yaml:"forbid,omitempty" json:"forbid,omitempty"`
	ExcludeGodocExamples bool               `yaml:"exclude-godoc-examples,omitempty" json:"exclude-godoc-examples,omitempty"`
	AnalyzeTypes         bool               `yaml:"analyze-types,omitempty" json:"analyze-types,omitempty"`
}

// ForbidigoPattern is one entry in forbidigo.forbid. YAML representation
// is polymorphic: either a bare string pattern, or a {p, pkg, msg}
// mapping. See [ForbidigoPattern.UnmarshalYAML].
type ForbidigoPattern struct {
	Pattern string `yaml:"p,omitempty" json:"p,omitempty"`
	Package string `yaml:"pkg,omitempty" json:"pkg,omitempty"`
	Msg     string `yaml:"msg,omitempty" json:"msg,omitempty"`
}

// FuncOrderSettings — `funcorder` linter.
type FuncOrderSettings struct {
	Constructor  bool `yaml:"constructor,omitempty" json:"constructor,omitempty"`
	StructMethod bool `yaml:"struct-method,omitempty" json:"struct-method,omitempty"`
	Alphabetical bool `yaml:"alphabetical,omitempty" json:"alphabetical,omitempty"`
}

// FunlenSettings — `funlen` linter.
type FunlenSettings struct {
	Lines          int  `yaml:"lines,omitempty" json:"lines,omitempty"`
	Statements     int  `yaml:"statements,omitempty" json:"statements,omitempty"`
	IgnoreComments bool `yaml:"ignore-comments,omitempty" json:"ignore-comments,omitempty"`
}

// GinkgoLinterSettings — `ginkgolinter` linter.
type GinkgoLinterSettings struct {
	SuppressLenAssertion       bool `yaml:"suppress-len-assertion,omitempty" json:"suppress-len-assertion,omitempty"`
	SuppressNilAssertion       bool `yaml:"suppress-nil-assertion,omitempty" json:"suppress-nil-assertion,omitempty"`
	SuppressErrAssertion       bool `yaml:"suppress-err-assertion,omitempty" json:"suppress-err-assertion,omitempty"`
	SuppressCompareAssertion   bool `yaml:"suppress-compare-assertion,omitempty" json:"suppress-compare-assertion,omitempty"`
	SuppressAsyncAssertion     bool `yaml:"suppress-async-assertion,omitempty" json:"suppress-async-assertion,omitempty"`
	SuppressTypeCompareWarning bool `yaml:"suppress-type-compare-assertion,omitempty" json:"suppress-type-compare-assertion,omitempty"`
	ForbidFocusContainer       bool `yaml:"forbid-focus-container,omitempty" json:"forbid-focus-container,omitempty"`
	AllowHaveLenZero           bool `yaml:"allow-havelen-zero,omitempty" json:"allow-havelen-zero,omitempty"`
	ForceExpectTo              bool `yaml:"force-expect-to,omitempty" json:"force-expect-to,omitempty"`
	ValidateAsyncIntervals     bool `yaml:"validate-async-intervals,omitempty" json:"validate-async-intervals,omitempty"`
	ForbidSpecPollution        bool `yaml:"forbid-spec-pollution,omitempty" json:"forbid-spec-pollution,omitempty"`
	ForceSucceedForFuncs       bool `yaml:"force-succeed,omitempty" json:"force-succeed,omitempty"`
	ForceAssertionDescription  bool `yaml:"force-assertion-description,omitempty" json:"force-assertion-description,omitempty"`
	ForeToNot                  bool `yaml:"force-tonot,omitempty" json:"force-tonot,omitempty"`
}

// GoChecksumTypeSettings — `gochecksumtype` linter.
type GoChecksumTypeSettings struct {
	DefaultSignifiesExhaustive bool `yaml:"default-signifies-exhaustive,omitempty" json:"default-signifies-exhaustive,omitempty"`
	IncludeSharedInterfaces    bool `yaml:"include-shared-interfaces,omitempty" json:"include-shared-interfaces,omitempty"`
}

// GocognitSettings — `gocognit` linter.
type GocognitSettings struct {
	MinComplexity int `yaml:"min-complexity,omitempty" json:"min-complexity,omitempty"`
}

// GoConstSettings — `goconst` linter.
type GoConstSettings struct {
	IgnoreStringValues   []string `yaml:"ignore-string-values,omitempty" json:"ignore-string-values,omitempty"`
	MatchWithConstants   bool     `yaml:"match-constant,omitempty" json:"match-constant,omitempty"`
	MinStringLen         int      `yaml:"min-len,omitempty" json:"min-len,omitempty"`
	MinOccurrencesCount  int      `yaml:"min-occurrences,omitempty" json:"min-occurrences,omitempty"`
	ParseNumbers         bool     `yaml:"numbers,omitempty" json:"numbers,omitempty"`
	NumberMin            int      `yaml:"min,omitempty" json:"min,omitempty"`
	NumberMax            int      `yaml:"max,omitempty" json:"max,omitempty"`
	IgnoreCalls          bool     `yaml:"ignore-calls,omitempty" json:"ignore-calls,omitempty"`
	FindDuplicates       bool     `yaml:"find-duplicates,omitempty" json:"find-duplicates,omitempty"`
	EvalConstExpressions bool     `yaml:"eval-const-expressions,omitempty" json:"eval-const-expressions,omitempty"`
	// Deprecated: use IgnoreStringValues. Migrated by [Load].
	IgnoreStrings string `yaml:"ignore-strings,omitempty" json:"ignore-strings,omitempty"`
}

// GoCriticSettings — `gocritic` linter. `settings` is free-form per-check
// configuration; the inner map shape is owned by gocritic.
type GoCriticSettings struct {
	DisableAll       bool                             `yaml:"disable-all,omitempty" json:"disable-all,omitempty"`
	EnabledChecks    []string                         `yaml:"enabled-checks,omitempty" json:"enabled-checks,omitempty"`
	EnableAll        bool                             `yaml:"enable-all,omitempty" json:"enable-all,omitempty"`
	DisabledChecks  []string                         `yaml:"disabled-checks,omitempty" json:"disabled-checks,omitempty"`
	EnabledTags      []string                         `yaml:"enabled-tags,omitempty" json:"enabled-tags,omitempty"`
	DisabledTags     []string                         `yaml:"disabled-tags,omitempty" json:"disabled-tags,omitempty"`
	SettingsPerCheck map[string]GoCriticCheckSettings `yaml:"settings,omitempty" json:"settings,omitempty"`
	// Go is upstream-internal; populated from run.go at use-time.
	Go string `yaml:"-" json:"-"`
}

// GoCriticCheckSettings is the inner free-form map for one gocritic check.
type GoCriticCheckSettings map[string]any

// GoCycloSettings — `gocyclo` linter.
type GoCycloSettings struct {
	MinComplexity int `yaml:"min-complexity,omitempty" json:"min-complexity,omitempty"`
}

// GodoclintSettings — `godoclint` linter (nested options shape).
type GodoclintSettings struct {
	Default *string             `yaml:"default,omitempty" json:"default,omitempty"`
	Enable  []string            `yaml:"enable,omitempty" json:"enable,omitempty"`
	Disable []string            `yaml:"disable,omitempty" json:"disable,omitempty"`
	Options GodoclintOptions    `yaml:"options,omitempty" json:"options,omitempty"`
}

// GodoclintOptions — nested option groups under godoclint.
type GodoclintOptions struct {
	MaxLen        GodoclintMaxLen        `yaml:"max-len,omitempty" json:"max-len,omitempty"`
	RequireDoc    GodoclintRequireDoc    `yaml:"require-doc,omitempty" json:"require-doc,omitempty"`
	StartWithName GodoclintStartWithName `yaml:"start-with-name,omitempty" json:"start-with-name,omitempty"`
}

// GodoclintMaxLen — max-len sub-block.
type GodoclintMaxLen struct {
	Length *uint `yaml:"length,omitempty" json:"length,omitempty"`
}

// GodoclintRequireDoc — require-doc sub-block.
type GodoclintRequireDoc struct {
	IgnoreExported   *bool `yaml:"ignore-exported,omitempty" json:"ignore-exported,omitempty"`
	IgnoreUnexported *bool `yaml:"ignore-unexported,omitempty" json:"ignore-unexported,omitempty"`
}

// GodoclintStartWithName — start-with-name sub-block.
type GodoclintStartWithName struct {
	IncludeUnexported *bool `yaml:"include-unexported,omitempty" json:"include-unexported,omitempty"`
}

// GodotSettings — `godot` linter.
type GodotSettings struct {
	Scope   string   `yaml:"scope,omitempty" json:"scope,omitempty"`
	Exclude []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Capital bool     `yaml:"capital,omitempty" json:"capital,omitempty"`
	Period  bool     `yaml:"period,omitempty" json:"period,omitempty"`
}

// GodoxSettings — `godox` linter.
type GodoxSettings struct {
	Keywords []string `yaml:"keywords,omitempty" json:"keywords,omitempty"`
}

// GoHeaderSettings — `goheader` linter.
type GoHeaderSettings struct {
	Values       map[string]map[string]string `yaml:"values,omitempty" json:"values,omitempty"`
	Template     string                       `yaml:"template,omitempty" json:"template,omitempty"`
	TemplatePath string                       `yaml:"template-path,omitempty" json:"template-path,omitempty"`
}

// GoModDirectivesSettings — `gomoddirectives` linter.
type GoModDirectivesSettings struct {
	ReplaceAllowList          []string `yaml:"replace-allow-list,omitempty" json:"replace-allow-list,omitempty"`
	ReplaceLocal              bool     `yaml:"replace-local,omitempty" json:"replace-local,omitempty"`
	ExcludeForbidden          bool     `yaml:"exclude-forbidden,omitempty" json:"exclude-forbidden,omitempty"`
	RetractAllowNoExplanation bool     `yaml:"retract-allow-no-explanation,omitempty" json:"retract-allow-no-explanation,omitempty"`
	ToolchainForbidden        bool     `yaml:"toolchain-forbidden,omitempty" json:"toolchain-forbidden,omitempty"`
	ToolchainPattern          string   `yaml:"toolchain-pattern,omitempty" json:"toolchain-pattern,omitempty"`
	ToolForbidden             bool     `yaml:"tool-forbidden,omitempty" json:"tool-forbidden,omitempty"`
	GoDebugForbidden          bool     `yaml:"go-debug-forbidden,omitempty" json:"go-debug-forbidden,omitempty"`
	GoVersionPattern          string   `yaml:"go-version-pattern,omitempty" json:"go-version-pattern,omitempty"`
	CheckModulePath           bool     `yaml:"check-module-path,omitempty" json:"check-module-path,omitempty"`
}

// GoModGuardSettings — `gomodguard` linter.
type GoModGuardSettings struct {
	Allowed GoModGuardAllowed `yaml:"allowed,omitempty" json:"allowed,omitempty"`
	Blocked GoModGuardBlocked `yaml:"blocked,omitempty" json:"blocked,omitempty"`
}

// GoModGuardAllowed — gomodguard.allowed block.
type GoModGuardAllowed struct {
	Modules []string `yaml:"modules,omitempty" json:"modules,omitempty"`
	Domains []string `yaml:"domains,omitempty" json:"domains,omitempty"`
}

// GoModGuardBlocked — gomodguard.blocked block.
type GoModGuardBlocked struct {
	Modules                []map[string]GoModGuardModule  `yaml:"modules,omitempty" json:"modules,omitempty"`
	Versions               []map[string]GoModGuardVersion `yaml:"versions,omitempty" json:"versions,omitempty"`
	LocalReplaceDirectives bool                           `yaml:"local-replace-directives,omitempty" json:"local-replace-directives,omitempty"`
}

// GoModGuardModule — gomodguard.blocked.modules entry value.
type GoModGuardModule struct {
	Recommendations []string `yaml:"recommendations,omitempty" json:"recommendations,omitempty"`
	Reason          string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// GoModGuardVersion — gomodguard.blocked.versions entry value.
type GoModGuardVersion struct {
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	Reason  string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// GoSecSettings — `gosec` linter.
type GoSecSettings struct {
	Includes    []string       `yaml:"includes,omitempty" json:"includes,omitempty"`
	Excludes    []string       `yaml:"excludes,omitempty" json:"excludes,omitempty"`
	Severity    string         `yaml:"severity,omitempty" json:"severity,omitempty"`
	Confidence  string         `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	Config      map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
	Concurrency int            `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
}

// GosmopolitanSettings — `gosmopolitan` linter.
type GosmopolitanSettings struct {
	AllowTimeLocal  bool     `yaml:"allow-time-local,omitempty" json:"allow-time-local,omitempty"`
	EscapeHatches   []string `yaml:"escape-hatches,omitempty" json:"escape-hatches,omitempty"`
	WatchForScripts []string `yaml:"watch-for-scripts,omitempty" json:"watch-for-scripts,omitempty"`
}

// GovetSettings — `govet` linter. Validates enable-all/disable-all
// mutual exclusivity.
type GovetSettings struct {
	Enable     []string                  `yaml:"enable,omitempty" json:"enable,omitempty"`
	Disable    []string                  `yaml:"disable,omitempty" json:"disable,omitempty"`
	EnableAll  bool                      `yaml:"enable-all,omitempty" json:"enable-all,omitempty"`
	DisableAll bool                      `yaml:"disable-all,omitempty" json:"disable-all,omitempty"`
	Settings   map[string]map[string]any `yaml:"settings,omitempty" json:"settings,omitempty"`
	// Go is upstream-internal; populated from run.go at use-time.
	Go string `yaml:"-" json:"-"`
}

// Validate checks govet enable/disable combinations.
func (cfg *GovetSettings) Validate() error {
	if cfg.EnableAll && cfg.DisableAll {
		return errors.New("govet: enable-all and disable-all cannot both be true")
	}
	if cfg.EnableAll && len(cfg.Enable) != 0 {
		return errors.New("govet: enable-all and enable cannot both be set")
	}
	if cfg.DisableAll && len(cfg.Disable) != 0 {
		return errors.New("govet: disable-all and disable cannot both be set")
	}
	return nil
}

// GrouperSettings — `grouper` linter.
type GrouperSettings struct {
	ConstRequireSingleConst   bool `yaml:"const-require-single-const,omitempty" json:"const-require-single-const,omitempty"`
	ConstRequireGrouping      bool `yaml:"const-require-grouping,omitempty" json:"const-require-grouping,omitempty"`
	ImportRequireSingleImport bool `yaml:"import-require-single-import,omitempty" json:"import-require-single-import,omitempty"`
	ImportRequireGrouping     bool `yaml:"import-require-grouping,omitempty" json:"import-require-grouping,omitempty"`
	TypeRequireSingleType     bool `yaml:"type-require-single-type,omitempty" json:"type-require-single-type,omitempty"`
	TypeRequireGrouping       bool `yaml:"type-require-grouping,omitempty" json:"type-require-grouping,omitempty"`
	VarRequireSingleVar       bool `yaml:"var-require-single-var,omitempty" json:"var-require-single-var,omitempty"`
	VarRequireGrouping        bool `yaml:"var-require-grouping,omitempty" json:"var-require-grouping,omitempty"`
}

// IfaceSettings — `iface` linter.
type IfaceSettings struct {
	Enable   []string                  `yaml:"enable,omitempty" json:"enable,omitempty"`
	Settings map[string]map[string]any `yaml:"settings,omitempty" json:"settings,omitempty"`
}

// ImportAsSettings — `importas` linter.
type ImportAsSettings struct {
	Alias          []ImportAsAlias `yaml:"alias,omitempty" json:"alias,omitempty"`
	NoUnaliased    bool            `yaml:"no-unaliased,omitempty" json:"no-unaliased,omitempty"`
	NoExtraAliases bool            `yaml:"no-extra-aliases,omitempty" json:"no-extra-aliases,omitempty"`
}

// ImportAsAlias is one entry in importas.alias.
type ImportAsAlias struct {
	Pkg   string `yaml:"pkg,omitempty" json:"pkg,omitempty"`
	Alias string `yaml:"alias,omitempty" json:"alias,omitempty"`
}

// INamedParamSettings — `inamedparam` linter.
type INamedParamSettings struct {
	SkipSingleParam bool `yaml:"skip-single-param,omitempty" json:"skip-single-param,omitempty"`
}

// IneffassignSettings — `ineffassign` linter.
type IneffassignSettings struct {
	CheckEscapingErrors bool `yaml:"check-escaping-errors,omitempty" json:"check-escaping-errors,omitempty"`
}

// InterfaceBloatSettings — `interfacebloat` linter.
type InterfaceBloatSettings struct {
	Max int `yaml:"max,omitempty" json:"max,omitempty"`
}

// IotaMixingSettings — `iotamixing` linter.
type IotaMixingSettings struct {
	ReportIndividual bool `yaml:"report-individual,omitempty" json:"report-individual,omitempty"`
}

// IreturnSettings — `ireturn` linter.
type IreturnSettings struct {
	Allow  []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	Reject []string `yaml:"reject,omitempty" json:"reject,omitempty"`
}

// LllSettings — `lll` linter.
type LllSettings struct {
	LineLength int `yaml:"line-length,omitempty" json:"line-length,omitempty"`
	TabWidth   int `yaml:"tab-width,omitempty" json:"tab-width,omitempty"`
}

// LoggerCheckSettings — `loggercheck` linter.
type LoggerCheckSettings struct {
	Kitlog           bool     `yaml:"kitlog,omitempty" json:"kitlog,omitempty"`
	Klog             bool     `yaml:"klog,omitempty" json:"klog,omitempty"`
	Logr             bool     `yaml:"logr,omitempty" json:"logr,omitempty"`
	Slog             bool     `yaml:"slog,omitempty" json:"slog,omitempty"`
	Zap              bool     `yaml:"zap,omitempty" json:"zap,omitempty"`
	RequireStringKey bool     `yaml:"require-string-key,omitempty" json:"require-string-key,omitempty"`
	NoPrintfLike     bool     `yaml:"no-printf-like,omitempty" json:"no-printf-like,omitempty"`
	Rules            []string `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// MaintIdxSettings — `maintidx` linter.
type MaintIdxSettings struct {
	Under int `yaml:"under,omitempty" json:"under,omitempty"`
}

// MakezeroSettings — `makezero` linter.
type MakezeroSettings struct {
	Always bool `yaml:"always,omitempty" json:"always,omitempty"`
}

// MisspellSettings — `misspell` linter.
type MisspellSettings struct {
	Mode        string               `yaml:"mode,omitempty" json:"mode,omitempty"`
	Locale      string               `yaml:"locale,omitempty" json:"locale,omitempty"`
	ExtraWords  []MisspellExtraWords `yaml:"extra-words,omitempty" json:"extra-words,omitempty"`
	IgnoreRules []string             `yaml:"ignore-rules,omitempty" json:"ignore-rules,omitempty"`
}

// MisspellExtraWords is one entry in misspell.extra-words.
type MisspellExtraWords struct {
	Typo       string `yaml:"typo,omitempty" json:"typo,omitempty"`
	Correction string `yaml:"correction,omitempty" json:"correction,omitempty"`
}

// MustTagSettings — `musttag` linter.
type MustTagSettings struct {
	Functions []MustTagFunction `yaml:"functions,omitempty" json:"functions,omitempty"`
}

// MustTagFunction is one entry in musttag.functions.
type MustTagFunction struct {
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Tag    string `yaml:"tag,omitempty" json:"tag,omitempty"`
	ArgPos int    `yaml:"arg-pos,omitempty" json:"arg-pos,omitempty"`
}

// NakedretSettings — `nakedret` linter.
type NakedretSettings struct {
	MaxFuncLines uint `yaml:"max-func-lines,omitempty" json:"max-func-lines,omitempty"`
}

// NestifSettings — `nestif` linter.
type NestifSettings struct {
	MinComplexity int `yaml:"min-complexity,omitempty" json:"min-complexity,omitempty"`
}

// NilNilSettings — `nilnil` linter.
type NilNilSettings struct {
	OnlyTwo        *bool    `yaml:"only-two,omitempty" json:"only-two,omitempty"`
	DetectOpposite bool     `yaml:"detect-opposite,omitempty" json:"detect-opposite,omitempty"`
	CheckedTypes   []string `yaml:"checked-types,omitempty" json:"checked-types,omitempty"`
}

// NlreturnSettings — `nlreturn` linter.
type NlreturnSettings struct {
	BlockSize int `yaml:"block-size,omitempty" json:"block-size,omitempty"`
}

// MndSettings — `mnd` linter.
type MndSettings struct {
	Checks           []string `yaml:"checks,omitempty" json:"checks,omitempty"`
	IgnoredNumbers   []string `yaml:"ignored-numbers,omitempty" json:"ignored-numbers,omitempty"`
	IgnoredFiles     []string `yaml:"ignored-files,omitempty" json:"ignored-files,omitempty"`
	IgnoredFunctions []string `yaml:"ignored-functions,omitempty" json:"ignored-functions,omitempty"`
}

// ModernizeSettings — `modernize` linter.
type ModernizeSettings struct {
	Disable []string `yaml:"disable,omitempty" json:"disable,omitempty"`
}

// NoLintLintSettings — `nolintlint` linter.
type NoLintLintSettings struct {
	RequireExplanation bool     `yaml:"require-explanation,omitempty" json:"require-explanation,omitempty"`
	RequireSpecific    bool     `yaml:"require-specific,omitempty" json:"require-specific,omitempty"`
	AllowNoExplanation []string `yaml:"allow-no-explanation,omitempty" json:"allow-no-explanation,omitempty"`
	AllowUnused        bool     `yaml:"allow-unused,omitempty" json:"allow-unused,omitempty"`
}

// NoNamedReturnsSettings — `nonamedreturns` linter.
type NoNamedReturnsSettings struct {
	ReportErrorInDefer bool `yaml:"report-error-in-defer,omitempty" json:"report-error-in-defer,omitempty"`
}

// ParallelTestSettings — `paralleltest` linter.
type ParallelTestSettings struct {
	IgnoreMissing         bool `yaml:"ignore-missing,omitempty" json:"ignore-missing,omitempty"`
	IgnoreMissingSubtests bool `yaml:"ignore-missing-subtests,omitempty" json:"ignore-missing-subtests,omitempty"`
	// Go is upstream-internal; populated from run.go at use-time.
	Go string `yaml:"-" json:"-"`
}

// PerfSprintSettings — `perfsprint` linter.
type PerfSprintSettings struct {
	IntegerFormat bool `yaml:"integer-format,omitempty" json:"integer-format,omitempty"`
	IntConversion bool `yaml:"int-conversion,omitempty" json:"int-conversion,omitempty"`
	ErrorFormat   bool `yaml:"error-format,omitempty" json:"error-format,omitempty"`
	ErrError      bool `yaml:"err-error,omitempty" json:"err-error,omitempty"`
	ErrorF        bool `yaml:"errorf,omitempty" json:"errorf,omitempty"`
	StringFormat  bool `yaml:"string-format,omitempty" json:"string-format,omitempty"`
	SprintF1      bool `yaml:"sprintf1,omitempty" json:"sprintf1,omitempty"`
	StrConcat     bool `yaml:"strconcat,omitempty" json:"strconcat,omitempty"`
	BoolFormat    bool `yaml:"bool-format,omitempty" json:"bool-format,omitempty"`
	HexFormat     bool `yaml:"hex-format,omitempty" json:"hex-format,omitempty"`
	ConcatLoop    bool `yaml:"concat-loop,omitempty" json:"concat-loop,omitempty"`
	LoopOtherOps  bool `yaml:"loop-other-ops,omitempty" json:"loop-other-ops,omitempty"`
}

// PreallocSettings — `prealloc` linter.
type PreallocSettings struct {
	Simple     bool `yaml:"simple,omitempty" json:"simple,omitempty"`
	RangeLoops bool `yaml:"range-loops,omitempty" json:"range-loops,omitempty"`
	ForLoops   bool `yaml:"for-loops,omitempty" json:"for-loops,omitempty"`
}

// PredeclaredSettings — `predeclared` linter.
type PredeclaredSettings struct {
	Ignore    []string `yaml:"ignore,omitempty" json:"ignore,omitempty"`
	Qualified bool     `yaml:"qualified-name,omitempty" json:"qualified-name,omitempty"`
}

// PromlinterSettings — `promlinter` linter.
type PromlinterSettings struct {
	Strict          bool     `yaml:"strict,omitempty" json:"strict,omitempty"`
	DisabledLinters []string `yaml:"disabled-linters,omitempty" json:"disabled-linters,omitempty"`
}

// ProtoGetterSettings — `protogetter` linter.
type ProtoGetterSettings struct {
	SkipGeneratedBy         []string `yaml:"skip-generated-by,omitempty" json:"skip-generated-by,omitempty"`
	SkipFiles               []string `yaml:"skip-files,omitempty" json:"skip-files,omitempty"`
	SkipAnyGenerated        bool     `yaml:"skip-any-generated,omitempty" json:"skip-any-generated,omitempty"`
	ReplaceFirstArgInAppend bool     `yaml:"replace-first-arg-in-append,omitempty" json:"replace-first-arg-in-append,omitempty"`
}

// ReassignSettings — `reassign` linter.
type ReassignSettings struct {
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
}

// RecvcheckSettings — `recvcheck` linter.
type RecvcheckSettings struct {
	DisableBuiltin bool     `yaml:"disable-builtin,omitempty" json:"disable-builtin,omitempty"`
	Exclusions     []string `yaml:"exclusions,omitempty" json:"exclusions,omitempty"`
}

// ReviveSettings — `revive` linter. `rules` is order-sensitive (rule
// precedence in revive evaluation).
type ReviveSettings struct {
	MaxOpenFiles       int               `yaml:"max-open-files,omitempty" json:"max-open-files,omitempty"`
	Confidence         float64           `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	Severity           string            `yaml:"severity,omitempty" json:"severity,omitempty"`
	EnableAllRules     bool              `yaml:"enable-all-rules,omitempty" json:"enable-all-rules,omitempty"`
	EnableDefaultRules bool              `yaml:"enable-default-rules,omitempty" json:"enable-default-rules,omitempty"`
	Rules              []ReviveRule      `yaml:"rules,omitempty" json:"rules,omitempty"`
	ErrorCode          int               `yaml:"error-code,omitempty" json:"error-code,omitempty"`
	WarningCode        int               `yaml:"warning-code,omitempty" json:"warning-code,omitempty"`
	Directives         []ReviveDirective `yaml:"directives,omitempty" json:"directives,omitempty"`
	// Go is upstream-internal; populated from run.go at use-time.
	Go string `yaml:"-" json:"-"`
}

// ReviveRule is one entry in revive.rules. Arguments is intentionally
// `[]any` because revive lets each rule define its own arg shape.
type ReviveRule struct {
	Name      string   `yaml:"name,omitempty" json:"name,omitempty"`
	Arguments []any    `yaml:"arguments,omitempty" json:"arguments,omitempty"`
	Severity  string   `yaml:"severity,omitempty" json:"severity,omitempty"`
	Disabled  bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Exclude   []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

// ReviveDirective is one entry in revive.directives.
type ReviveDirective struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	Severity string `yaml:"severity,omitempty" json:"severity,omitempty"`
}

// RowsErrCheckSettings — `rowserrcheck` linter.
type RowsErrCheckSettings struct {
	Packages []string `yaml:"packages,omitempty" json:"packages,omitempty"`
}

// SlogLintSettings — `sloglint` linter.
type SlogLintSettings struct {
	NoMixedArgs    bool     `yaml:"no-mixed-args,omitempty" json:"no-mixed-args,omitempty"`
	KVOnly         bool     `yaml:"kv-only,omitempty" json:"kv-only,omitempty"`
	AttrOnly       bool     `yaml:"attr-only,omitempty" json:"attr-only,omitempty"`
	NoGlobal       string   `yaml:"no-global,omitempty" json:"no-global,omitempty"`
	Context        string   `yaml:"context,omitempty" json:"context,omitempty"`
	StaticMsg      bool     `yaml:"static-msg,omitempty" json:"static-msg,omitempty"`
	MsgStyle       string   `yaml:"msg-style,omitempty" json:"msg-style,omitempty"`
	NoRawKeys      bool     `yaml:"no-raw-keys,omitempty" json:"no-raw-keys,omitempty"`
	KeyNamingCase  string   `yaml:"key-naming-case,omitempty" json:"key-naming-case,omitempty"`
	ForbiddenKeys  []string `yaml:"forbidden-keys,omitempty" json:"forbidden-keys,omitempty"`
	ArgsOnSepLines bool     `yaml:"args-on-sep-lines,omitempty" json:"args-on-sep-lines,omitempty"`
}

// SpancheckSettings — `spancheck` linter.
type SpancheckSettings struct {
	Checks                   []string `yaml:"checks,omitempty" json:"checks,omitempty"`
	IgnoreCheckSignatures    []string `yaml:"ignore-check-signatures,omitempty" json:"ignore-check-signatures,omitempty"`
	ExtraStartSpanSignatures []string `yaml:"extra-start-span-signatures,omitempty" json:"extra-start-span-signatures,omitempty"`
}

// StaticCheckSettings — `staticcheck` linter. Carries legacy fields
// that gosimple and stylecheck used to populate before v2 consolidation.
type StaticCheckSettings struct {
	Checks                  []string `yaml:"checks,omitempty" json:"checks,omitempty"`
	Initialisms             []string `yaml:"initialisms,omitempty" json:"initialisms,omitempty"`                               // only for stylecheck
	DotImportWhitelist      []string `yaml:"dot-import-whitelist,omitempty" json:"dot-import-whitelist,omitempty"`             // only for stylecheck
	HTTPStatusCodeWhitelist []string `yaml:"http-status-code-whitelist,omitempty" json:"http-status-code-whitelist,omitempty"` // only for stylecheck
}

// TagAlignSettings — `tagalign` linter.
type TagAlignSettings struct {
	Align  bool     `yaml:"align,omitempty" json:"align,omitempty"`
	Sort   bool     `yaml:"sort,omitempty" json:"sort,omitempty"`
	Order  []string `yaml:"order,omitempty" json:"order,omitempty"`
	Strict bool     `yaml:"strict,omitempty" json:"strict,omitempty"`
}

// TagliatelleSettings — `tagliatelle` linter.
type TagliatelleSettings struct {
	Case TagliatelleCase `yaml:"case,omitempty" json:"case,omitempty"`
}

// TagliatelleCase — tagliatelle.case block.
type TagliatelleCase struct {
	TagliatelleBase `yaml:",inline" json:",inline"`
	Overrides       []TagliatelleOverrides `yaml:"overrides,omitempty" json:"overrides,omitempty"`
}

// TagliatelleOverrides — one entry in tagliatelle.case.overrides.
type TagliatelleOverrides struct {
	TagliatelleBase `yaml:",inline" json:",inline"`
	Package         string `yaml:"pkg,omitempty" json:"pkg,omitempty"`
	Ignore          bool   `yaml:"ignore,omitempty" json:"ignore,omitempty"`
}

// TagliatelleBase — common fields under tagliatelle.case.
type TagliatelleBase struct {
	Rules         map[string]string                  `yaml:"rules,omitempty" json:"rules,omitempty"`
	ExtendedRules map[string]TagliatelleExtendedRule `yaml:"extended-rules,omitempty" json:"extended-rules,omitempty"`
	UseFieldName  bool                               `yaml:"use-field-name,omitempty" json:"use-field-name,omitempty"`
	IgnoredFields []string                           `yaml:"ignored-fields,omitempty" json:"ignored-fields,omitempty"`
}

// TagliatelleExtendedRule — one entry in tagliatelle.extended-rules.
type TagliatelleExtendedRule struct {
	Case                string          `yaml:"case,omitempty" json:"case,omitempty"`
	ExtraInitialisms    bool            `yaml:"extra-initialisms,omitempty" json:"extra-initialisms,omitempty"`
	InitialismOverrides map[string]bool `yaml:"initialism-overrides,omitempty" json:"initialism-overrides,omitempty"`
}

// TestifylintSettings — `testifylint` linter.
type TestifylintSettings struct {
	EnableAll        bool     `yaml:"enable-all,omitempty" json:"enable-all,omitempty"`
	DisableAll       bool     `yaml:"disable-all,omitempty" json:"disable-all,omitempty"`
	EnabledCheckers  []string `yaml:"enable,omitempty" json:"enable,omitempty"`
	DisabledCheckers []string `yaml:"disable,omitempty" json:"disable,omitempty"`

	BoolCompare          TestifylintBoolCompare          `yaml:"bool-compare,omitempty" json:"bool-compare,omitempty"`
	ExpectedActual       TestifylintExpectedActual       `yaml:"expected-actual,omitempty" json:"expected-actual,omitempty"`
	Formatter            TestifylintFormatter            `yaml:"formatter,omitempty" json:"formatter,omitempty"`
	GoRequire            TestifylintGoRequire            `yaml:"go-require,omitempty" json:"go-require,omitempty"`
	RequireError         TestifylintRequireError         `yaml:"require-error,omitempty" json:"require-error,omitempty"`
	SuiteExtraAssertCall TestifylintSuiteExtraAssertCall `yaml:"suite-extra-assert-call,omitempty" json:"suite-extra-assert-call,omitempty"`
}

// TestifylintBoolCompare — testifylint.bool-compare.
type TestifylintBoolCompare struct {
	IgnoreCustomTypes bool `yaml:"ignore-custom-types,omitempty" json:"ignore-custom-types,omitempty"`
}

// TestifylintExpectedActual — testifylint.expected-actual.
type TestifylintExpectedActual struct {
	ExpVarPattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
}

// TestifylintFormatter — testifylint.formatter.
type TestifylintFormatter struct {
	CheckFormatString *bool `yaml:"check-format-string,omitempty" json:"check-format-string,omitempty"`
	RequireFFuncs     bool  `yaml:"require-f-funcs,omitempty" json:"require-f-funcs,omitempty"`
	RequireStringMsg  bool  `yaml:"require-string-msg,omitempty" json:"require-string-msg,omitempty"`
}

// TestifylintGoRequire — testifylint.go-require.
type TestifylintGoRequire struct {
	IgnoreHTTPHandlers bool `yaml:"ignore-http-handlers,omitempty" json:"ignore-http-handlers,omitempty"`
}

// TestifylintRequireError — testifylint.require-error.
type TestifylintRequireError struct {
	FnPattern string `yaml:"fn-pattern,omitempty" json:"fn-pattern,omitempty"`
}

// TestifylintSuiteExtraAssertCall — testifylint.suite-extra-assert-call.
type TestifylintSuiteExtraAssertCall struct {
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// TestpackageSettings — `testpackage` linter.
type TestpackageSettings struct {
	SkipRegexp    string   `yaml:"skip-regexp,omitempty" json:"skip-regexp,omitempty"`
	AllowPackages []string `yaml:"allow-packages,omitempty" json:"allow-packages,omitempty"`
}

// ThelperSettings — `thelper` linter.
type ThelperSettings struct {
	Test      ThelperOptions `yaml:"test,omitempty" json:"test,omitempty"`
	Fuzz      ThelperOptions `yaml:"fuzz,omitempty" json:"fuzz,omitempty"`
	Benchmark ThelperOptions `yaml:"benchmark,omitempty" json:"benchmark,omitempty"`
	TB        ThelperOptions `yaml:"tb,omitempty" json:"tb,omitempty"`
}

// ThelperOptions — thelper.test/fuzz/benchmark/tb sub-block.
type ThelperOptions struct {
	First *bool `yaml:"first,omitempty" json:"first,omitempty"`
	Name  *bool `yaml:"name,omitempty" json:"name,omitempty"`
	Begin *bool `yaml:"begin,omitempty" json:"begin,omitempty"`
}

// UseStdlibVarsSettings — `usestdlibvars` linter.
type UseStdlibVarsSettings struct {
	HTTPMethod         bool `yaml:"http-method,omitempty" json:"http-method,omitempty"`
	HTTPStatusCode     bool `yaml:"http-status-code,omitempty" json:"http-status-code,omitempty"`
	TimeWeekday        bool `yaml:"time-weekday,omitempty" json:"time-weekday,omitempty"`
	TimeMonth          bool `yaml:"time-month,omitempty" json:"time-month,omitempty"`
	TimeLayout         bool `yaml:"time-layout,omitempty" json:"time-layout,omitempty"`
	CryptoHash         bool `yaml:"crypto-hash,omitempty" json:"crypto-hash,omitempty"`
	DefaultRPCPath     bool `yaml:"default-rpc-path,omitempty" json:"default-rpc-path,omitempty"`
	SQLIsolationLevel  bool `yaml:"sql-isolation-level,omitempty" json:"sql-isolation-level,omitempty"`
	TLSSignatureScheme bool `yaml:"tls-signature-scheme,omitempty" json:"tls-signature-scheme,omitempty"`
	ConstantKind       bool `yaml:"constant-kind,omitempty" json:"constant-kind,omitempty"`
	TimeDateMonth      bool `yaml:"time-date-month,omitempty" json:"time-date-month,omitempty"`
}

// UseTestingSettings — `usetesting` linter.
type UseTestingSettings struct {
	ContextBackground bool `yaml:"context-background,omitempty" json:"context-background,omitempty"`
	ContextTodo       bool `yaml:"context-todo,omitempty" json:"context-todo,omitempty"`
	OSChdir           bool `yaml:"os-chdir,omitempty" json:"os-chdir,omitempty"`
	OSMkdirTemp       bool `yaml:"os-mkdir-temp,omitempty" json:"os-mkdir-temp,omitempty"`
	OSSetenv          bool `yaml:"os-setenv,omitempty" json:"os-setenv,omitempty"`
	OSTempDir         bool `yaml:"os-temp-dir,omitempty" json:"os-temp-dir,omitempty"`
	OSCreateTemp      bool `yaml:"os-create-temp,omitempty" json:"os-create-temp,omitempty"`
}

// UnconvertSettings — `unconvert` linter.
type UnconvertSettings struct {
	FastMath bool `yaml:"fast-math,omitempty" json:"fast-math,omitempty"`
	Safe     bool `yaml:"safe,omitempty" json:"safe,omitempty"`
}

// UnparamSettings — `unparam` linter.
type UnparamSettings struct {
	CheckExported bool `yaml:"check-exported,omitempty" json:"check-exported,omitempty"`
}

// UnqueryvetSettings — `unqueryvet` linter.
type UnqueryvetSettings struct {
	CheckSQLBuilders     bool                          `yaml:"check-sql-builders,omitempty" json:"check-sql-builders,omitempty"`
	AllowedPatterns      []string                      `yaml:"allowed-patterns,omitempty" json:"allowed-patterns,omitempty"`
	IgnoredFunctions     []string                      `yaml:"ignored-functions,omitempty" json:"ignored-functions,omitempty"`
	CheckAliasedWildcard bool                          `yaml:"check-aliased-wildcard,omitempty" json:"check-aliased-wildcard,omitempty"`
	CheckStringConcat    bool                          `yaml:"check-string-concat,omitempty" json:"check-string-concat,omitempty"`
	CheckFormatStrings   bool                          `yaml:"check-format-strings,omitempty" json:"check-format-strings,omitempty"`
	CheckStringBuilder   bool                          `yaml:"check-string-builder,omitempty" json:"check-string-builder,omitempty"`
	CheckSubqueries      bool                          `yaml:"check-subqueries,omitempty" json:"check-subqueries,omitempty"`
	CheckN1              bool                          `yaml:"check-n1,omitempty" json:"check-n1,omitempty"`
	CheckSQLInjection    bool                          `yaml:"check-sql-injection,omitempty" json:"check-sql-injection,omitempty"`
	CheckTxLeak          bool                          `yaml:"check-tx-leaks,omitempty" json:"check-tx-leaks,omitempty"`
	SQLBuilders          UnqueryvetSQLBuildersSettings `yaml:"sql-builders,omitempty" json:"sql-builders,omitempty"`
	Allow                []string                      `yaml:"allow,omitempty" json:"allow,omitempty"`
	CustomRules          []UnqueryvetCustomRule        `yaml:"custom-rules,omitempty" json:"custom-rules,omitempty"`
}

// UnqueryvetSQLBuildersSettings — `unqueryvet.sql-builders` sub-block.
type UnqueryvetSQLBuildersSettings struct {
	Squirrel  bool `yaml:"squirrel,omitempty" json:"squirrel,omitempty"`
	GORM      bool `yaml:"gorm,omitempty" json:"gorm,omitempty"`
	SQLx      bool `yaml:"sqlx,omitempty" json:"sqlx,omitempty"`
	Ent       bool `yaml:"ent,omitempty" json:"ent,omitempty"`
	PGX       bool `yaml:"pgx,omitempty" json:"pgx,omitempty"`
	Bun       bool `yaml:"bun,omitempty" json:"bun,omitempty"`
	SQLBoiler bool `yaml:"sqlboiler,omitempty" json:"sqlboiler,omitempty"`
	Jet       bool `yaml:"jet,omitempty" json:"jet,omitempty"`
}

// UnqueryvetCustomRule — one entry in unqueryvet.custom-rules.
type UnqueryvetCustomRule struct {
	ID       string   `yaml:"id,omitempty" json:"id,omitempty"`
	Pattern  string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
	When     string   `yaml:"when,omitempty" json:"when,omitempty"`
	Message  string   `yaml:"message,omitempty" json:"message,omitempty"`
	Action   string   `yaml:"action,omitempty" json:"action,omitempty"`
}

// UnusedSettings — `unused` linter.
type UnusedSettings struct {
	FieldWritesAreUses     bool `yaml:"field-writes-are-uses,omitempty" json:"field-writes-are-uses,omitempty"`
	PostStatementsAreReads bool `yaml:"post-statements-are-reads,omitempty" json:"post-statements-are-reads,omitempty"`
	ExportedFieldsAreUsed  bool `yaml:"exported-fields-are-used,omitempty" json:"exported-fields-are-used,omitempty"`
	ParametersAreUsed      bool `yaml:"parameters-are-used,omitempty" json:"parameters-are-used,omitempty"`
	LocalVariablesAreUsed  bool `yaml:"local-variables-are-used,omitempty" json:"local-variables-are-used,omitempty"`
	GeneratedIsUsed        bool `yaml:"generated-is-used,omitempty" json:"generated-is-used,omitempty"`
}

// VarnamelenSettings — `varnamelen` linter.
type VarnamelenSettings struct {
	MaxDistance        int      `yaml:"max-distance,omitempty" json:"max-distance,omitempty"`
	MinNameLength      int      `yaml:"min-name-length,omitempty" json:"min-name-length,omitempty"`
	CheckReceiver      bool     `yaml:"check-receiver,omitempty" json:"check-receiver,omitempty"`
	CheckReturn        bool     `yaml:"check-return,omitempty" json:"check-return,omitempty"`
	CheckTypeParam     bool     `yaml:"check-type-param,omitempty" json:"check-type-param,omitempty"`
	IgnoreNames        []string `yaml:"ignore-names,omitempty" json:"ignore-names,omitempty"`
	IgnoreTypeAssertOk bool     `yaml:"ignore-type-assert-ok,omitempty" json:"ignore-type-assert-ok,omitempty"`
	IgnoreMapIndexOk   bool     `yaml:"ignore-map-index-ok,omitempty" json:"ignore-map-index-ok,omitempty"`
	IgnoreChanRecvOk   bool     `yaml:"ignore-chan-recv-ok,omitempty" json:"ignore-chan-recv-ok,omitempty"`
	IgnoreDecls        []string `yaml:"ignore-decls,omitempty" json:"ignore-decls,omitempty"`
}

// WhitespaceSettings — `whitespace` linter.
type WhitespaceSettings struct {
	MultiIf   bool `yaml:"multi-if,omitempty" json:"multi-if,omitempty"`
	MultiFunc bool `yaml:"multi-func,omitempty" json:"multi-func,omitempty"`
}

// WrapcheckSettings — `wrapcheck` linter.
type WrapcheckSettings struct {
	ExtraIgnoreSigs        []string `yaml:"extra-ignore-sigs,omitempty" json:"extra-ignore-sigs,omitempty"`
	IgnoreSigs             []string `yaml:"ignore-sigs,omitempty" json:"ignore-sigs,omitempty"`
	IgnoreSigRegexps       []string `yaml:"ignore-sig-regexps,omitempty" json:"ignore-sig-regexps,omitempty"`
	IgnorePackageGlobs     []string `yaml:"ignore-package-globs,omitempty" json:"ignore-package-globs,omitempty"`
	IgnoreInterfaceRegexps []string `yaml:"ignore-interface-regexps,omitempty" json:"ignore-interface-regexps,omitempty"`
	ReportInternalErrors   bool     `yaml:"report-internal-errors,omitempty" json:"report-internal-errors,omitempty"`
}

// WSLv4Settings — `wsl` v4. Deprecated: use [WSLv5Settings].
type WSLv4Settings struct {
	StrictAppend                     bool     `yaml:"strict-append,omitempty" json:"strict-append,omitempty"`
	AllowAssignAndCallCuddle         bool     `yaml:"allow-assign-and-call,omitempty" json:"allow-assign-and-call,omitempty"`
	AllowAssignAndAnythingCuddle     bool     `yaml:"allow-assign-and-anything,omitempty" json:"allow-assign-and-anything,omitempty"`
	AllowMultiLineAssignCuddle       bool     `yaml:"allow-multiline-assign,omitempty" json:"allow-multiline-assign,omitempty"`
	ForceCaseTrailingWhitespaceLimit int      `yaml:"force-case-trailing-whitespace,omitempty" json:"force-case-trailing-whitespace,omitempty"`
	AllowTrailingComment             bool     `yaml:"allow-trailing-comment,omitempty" json:"allow-trailing-comment,omitempty"`
	AllowSeparatedLeadingComment     bool     `yaml:"allow-separated-leading-comment,omitempty" json:"allow-separated-leading-comment,omitempty"`
	AllowCuddleDeclaration           bool     `yaml:"allow-cuddle-declarations,omitempty" json:"allow-cuddle-declarations,omitempty"`
	AllowCuddleWithCalls             []string `yaml:"allow-cuddle-with-calls,omitempty" json:"allow-cuddle-with-calls,omitempty"`
	AllowCuddleWithRHS               []string `yaml:"allow-cuddle-with-rhs,omitempty" json:"allow-cuddle-with-rhs,omitempty"`
	AllowCuddleUsedInBlock           bool     `yaml:"allow-cuddle-used-in-block,omitempty" json:"allow-cuddle-used-in-block,omitempty"`
	ForceCuddleErrCheckAndAssign     bool     `yaml:"force-err-cuddling,omitempty" json:"force-err-cuddling,omitempty"`
	ErrorVariableNames               []string `yaml:"error-variable-names,omitempty" json:"error-variable-names,omitempty"`
	ForceExclusiveShortDeclarations  bool     `yaml:"force-short-decl-cuddling,omitempty" json:"force-short-decl-cuddling,omitempty"`
}

// WSLv5Settings — `wsl_v5` linter.
type WSLv5Settings struct {
	AllowFirstInBlock bool     `yaml:"allow-first-in-block,omitempty" json:"allow-first-in-block,omitempty"`
	AllowWholeBlock   bool     `yaml:"allow-whole-block,omitempty" json:"allow-whole-block,omitempty"`
	BranchMaxLines    int      `yaml:"branch-max-lines,omitempty" json:"branch-max-lines,omitempty"`
	CaseMaxLines      int      `yaml:"case-max-lines,omitempty" json:"case-max-lines,omitempty"`
	Default           string   `yaml:"default,omitempty" json:"default,omitempty"`
	Enable            []string `yaml:"enable,omitempty" json:"enable,omitempty"`
	Disable           []string `yaml:"disable,omitempty" json:"disable,omitempty"`
}

// CustomLinterSettings configures a private linter plugin.
type CustomLinterSettings struct {
	Type        string `yaml:"type,omitempty" json:"type,omitempty"`
	Path        string `yaml:"path,omitempty" json:"path,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	OriginalURL string `yaml:"original-url,omitempty" json:"original-url,omitempty"`
	Settings    any    `yaml:"settings,omitempty" json:"settings,omitempty"`
}

// Validate checks plugin type-specific constraints.
func (s *CustomLinterSettings) Validate() error {
	if s.Type == "module" {
		if s.Path != "" {
			return errors.New("path is not supported with type=module")
		}
		return nil
	}
	if s.Path == "" {
		return errors.New("path is required")
	}
	return nil
}
