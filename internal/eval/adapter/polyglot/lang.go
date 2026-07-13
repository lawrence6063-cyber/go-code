// 本文件定义 polyglot 数据集六门语言在工作区内的测试运行方式（EVAL_SPEC §3/§5.2）。
package polyglot

// langSpec 描述一门语言在 polyglot 练习工作区内的测试命令。
type langSpec struct {
	name    string // 语言标识（go|python|rust|javascript|java|cpp）
	testCmd string // 在练习工作区根执行的测试命令（退出码 0=通过，非 0=未通过）
}

// supportedLangs 是 aider polyglot 数据集六门语言的测试命令，遵循各 Exercism track 的惯例。
// 命令均以「在练习工作区根目录（其目录名 = 练习 slug）执行、退出码即判据」为契约：
//   - python：显式跑 *_test.py，不依赖 pytest 发现配置；
//   - cpp：Exercism CMakeLists 由工作区目录名推导工程名/源文件名（故工作区须命名为 slug），
//     构建不自动跑测试，须执行产物 build/<slug>；EXERCISM_RUN_ALL_TESTS 开启全部用例；
//   - javascript：先 npm install 装 jest 依赖，再 npm test；
//   - java：用系统 gradle（`brew install gradle`）而非练习自带的 ./gradlew wrapper——wrapper 会从
//     services.gradle.org 拉取上百 MB 发行版，受限网络下大文件传输易被重置；系统 gradle 直接构建
//     标准 build.gradle（junit5 依赖走 mavenCentral，体积小得多）。
//
// 细节可能随数据集版本演进，落地时以各语言 track 当期约定为准（EVAL_SPEC §5.2.2 脚注）。
var supportedLangs = map[string]langSpec{
	"go":         {name: "go", testCmd: "go test ./..."},
	"python":     {name: "python", testCmd: "python3 -m pytest -q *_test.py"},
	"rust":       {name: "rust", testCmd: "cargo test --quiet"},
	"javascript": {name: "javascript", testCmd: "npm install --silent --no-audit --no-fund && npm test"},
	"java":       {name: "java", testCmd: "gradle test --console=plain --no-daemon"},
	"cpp":        {name: "cpp", testCmd: `cmake -S . -B build -DEXERCISM_RUN_ALL_TESTS=1 && cmake --build build && ./build/"$(basename "$PWD")"`},
}

// langOf 返回语言的测试规格；不支持的语言 ok=false。
func langOf(name string) (langSpec, bool) {
	spec, ok := supportedLangs[name]
	return spec, ok
}
