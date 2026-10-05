package analysis

// Exported aliases for testing unexported functions from the analysis_test
// external package.
var (
	LoadPackages         = loadPackages
	SelectDependencyPkgs = selectDependencyPkgs
	RecoverBuild         = recoverBuild
)
