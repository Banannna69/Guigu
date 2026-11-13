// extract.go：上传文档的纯文本抽取（.docx / .pdf 走 python，其余直读）。
// 主要函数/类型：
//   - ExtractText：按扩展名分发，返回文档纯文本。
//   - pythonExtract：把上传字节落到临时文件后交给 python 抽取。
//   - findPythonWith / pythonCandidates：探测「装了所需库」的 python 解释器。
//
// 为什么要探测解释器而不是直接用 python3：这是踩过的坑——PATH 里排在前面的
// homebrew python3 装了 python-docx，排后面的 pyenv python3 没装（或者反过来），
// 直接 `python3 -c "import docx"` 会随机失败，报错还像是「用户环境有问题」。
// 这里逐个候选探一次 import，选第一个能用的；全都不能用时仍返回第一个，
// 让调用方把 python 真实的 stderr（缺哪个模块）透出去，而不是吞成「没有 python」。

package knowledge

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExtractText 返回上传文档的纯文本。文本类格式直读，.docx/.pdf 走 python 抽取。
func ExtractText(path string, raw []byte) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt", ".text", ".csv", ".json", ".log", ".yaml", ".yml", ".htm", ".html", "":
		return string(raw), nil
	case ".docx":
		return pythonExtract(path, docxPy, "docx", raw)
	case ".pdf":
		return pythonExtract(path, pdfPy, "pdfplumber", raw)
	default:
		return "", fmt.Errorf("暂不支持的文件类型（支持 .md/.txt/.csv/.json/.docx/.pdf）")
	}
}

// docxPy 抽 docx：正文段落 + 表格（表格按行拼成 "a | b" 才能保住结构）。
const docxPy = `import sys
from docx import Document
d = Document(sys.argv[1])
parts = [p.text for p in d.paragraphs]
for t in d.tables:
    for r in t.rows:
        parts.append(" | ".join(c.text for c in r.cells))
print("\n".join(parts))
`

// pdfPy 抽 pdf：优先 pdfplumber（版式还原好），失败退回 pypdf/PyPDF2。
const pdfPy = `import sys
text = ""
try:
    import pdfplumber
    with pdfplumber.open(sys.argv[1]) as pdf:
        text = "\n".join((pg.extract_text() or "") for pg in pdf.pages)
except Exception:
    try:
        from pypdf import PdfReader
    except Exception:
        from PyPDF2 import PdfReader
    r = PdfReader(sys.argv[1])
    text = "\n".join((pg.extract_text() or "") for pg in r.pages)
print(text)
`

// pythonExtract 把上传字节写到临时文件，调 python 脚本抽取文本。
//
// 必须落临时文件：上传内容只在内存里（调用方给的是原始文件名而不是真实路径），
// python 的 docx/pdfplumber 都要按路径打开。
func pythonExtract(path, script, requireMod string, raw []byte) (string, error) {
	tmp, err := os.CreateTemp("", "kbdoc-*"+filepath.Ext(path))
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, werr := tmp.Write(raw); werr != nil {
		tmp.Close()
		return "", fmt.Errorf("写入临时文件失败: %w", werr)
	}
	tmp.Close()

	py := findPythonWith(requireMod)
	if py == "" {
		return "", fmt.Errorf("未找到 python3（解析 .docx/.pdf 需 python3 + python-docx / pdfplumber）")
	}
	cmd := exec.Command(py, "-c", script, tmp.Name())
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(errb.String())
		if len(detail) > 400 {
			detail = "…" + detail[len(detail)-400:]
		}
		return "", fmt.Errorf("解析失败（python=%s；如缺库请对该 python 执行 pip install python-docx pdfplumber）: %v: %s",
			filepath.Base(py), err, detail)
	}
	return string(out), nil
}

// findPythonWith 返回第一个能 import requireMod 的 python3；
// 一个都不能时仍返回第一个找到的，好让调用方拿到真实报错。
func findPythonWith(requireMod string) string {
	var first string
	for _, py := range pythonCandidates() {
		if first == "" {
			first = py
		}
		if exec.Command(py, "-c", "import "+requireMod).Run() == nil {
			return py
		}
	}
	return first
}

// pythonCandidates 列出待试的 python 解释器（去重）：先 PATH 解析的，
// 再几个常见绝对路径（pyenv shim / homebrew / framework / 系统）——
// 服务进程的 PATH 往往比交互 shell 更窄，只靠 PATH 会漏掉装了库的那个。
func pythonCandidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
			seen[p] = true
		}
	}
	for _, c := range []string{"python3", "python"} {
		if p, err := exec.LookPath(c); err == nil {
			add(p)
		}
	}
	home, _ := os.UserHomeDir()
	add(filepath.Join(home, ".pyenv", "shims", "python3"))
	add("/opt/homebrew/bin/python3")
	add("/usr/local/bin/python3")
	add("/usr/bin/python3")
	return out
}
