package walk

import (
	"path"
	"regexp"
	"strings"
)

// testDirs hold tests, fixtures or test data in common layouts.
var testDirs = map[string]bool{
	"test": true, "tests": true, "__tests__": true, "spec": true,
	"testdata": true, "fixtures": true, "__fixtures__": true, "__snapshots__": true,
}

// testName matches test files by the naming conventions of common languages:
// Go and Python _test, Python test_ and conftest, JS/TS .test and .spec,
// Ruby _spec, and Java/Kotlin/C# FooTest(s).
var testName = regexp.MustCompile(`(_test\.\w+|^test_.*\.py|^conftest\.py|\.(test|spec)\.[cm]?[jt]sx?|_spec\.rb|[a-z0-9]Tests?\.(java|kt|cs))$`)

// IsTest reports whether a slash path looks like a test file or test data.
// Agents searching for behaviour almost always want the implementation;
// tests describe the same behaviour and crowd it out of the results.
func IsTest(rel string) bool {
	dir, name := path.Split(rel)
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if testDirs[part] {
			return true
		}
	}
	return testName.MatchString(name)
}
