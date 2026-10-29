// A stub: a module of its own, so the root module's `./...` (build, test, lint, vet,
// deadcode) never walks node_modules, where packages ship Go files (flatted does).
module github.com/EugeneShtoka/kith/desktop

go 1.26.3
