#!/usr/bin/env python3
"""Checks that the release 1 documents only name things that exist on this branch.

    python3 tools/release1-docs-check/check.py              # check the documents (exit 1 on any problem)
    python3 tools/release1-docs-check/check.py --self-test  # prove the check notices planted mistakes

For docs/RELEASE1_RUNBOOK.md and backend/db/rehearsal.md it verifies:
  * every repository file or directory named anywhere in the text is tracked by git (a $variable or the N in
    verify_000N.sql matches any file of that shape);
  * every command in a code block starts with a known tool that is installed, or a tracked script;
  * `go run ./cmd/X` names a command that exists, with flags it declares; local_pg.sh is given a subcommand it has;
  * every variable in the runbook's environment table is read by the code or the console.
It reads files only. It cannot check dashboards, URLs or what a command does.
"""
import glob, os, re, shutil, subprocess, sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
DOCS = ["docs/RELEASE1_RUNBOOK.md", "backend/db/rehearsal.md"]
EXTENSIONS = "sql|sh|py|ts|tsx|go|md|yml|yaml|json|toml"
TOP = ("backend/", "frontend/", "docs/", "tools/", ".github/", "api/", "db/", "scripts/", "cmd/", "e2e/")
# Written by a step, or kept outside the repository: not expected to be tracked.
NOT_IN_REPO = re.compile(r"^(/tmp/|backend/backups(/|$)|\.\./helios-old|~/(?!helios-release1/)|\.pg_service|\.pgpass)|\.dump$")
TOOLS = {"git", "psql", "pg_restore", "pg_dump", "curl", "go", "diff", "python3", "tee"}
BUILTINS = {"cd", "for", "do", "done", "echo", "read", "export", "break", "wait", "time", "set"}
# Named in the runbook's table although no release 1 code reads them (the runbook says so).
IGNORED_ENV = {"HELIOS_ENV"}


def tracked():
    out = subprocess.run(["git", "-C", ROOT, "ls-files"], capture_output=True, text=True, check=True).stdout
    return out.split("\n")[:-1]


def path_tokens(text):
    for raw in re.findall(r"(?:<[^<>\n]*>/|~/|\.\./|/)?[A-Za-z0-9_.${}%*/-]+", text):
        tok = raw.rstrip(".,:")
        tok = re.sub(r"^<(YOUR REPO FOLDER)>/", "", tok)
        if tok.startswith("<") or "://" in tok:
            continue
        if re.search(r"\.(%s)$" % EXTENSIONS, tok) or (tok.startswith(TOP) and "/" in tok):
            yield tok


def exists(tok, files, dirs):
    tok = re.sub(r"^(~/helios-release1/|\./)", "", tok)
    if NOT_IN_REPO.search(tok):
        return True
    pattern = re.sub(r"\$\{[^}]*\}|\$[A-Za-z_]+|(?<=_000)N", "*", tok).rstrip("/")
    regex = re.compile(r"(^|.*/)" + re.escape(pattern).replace(r"\*", "[^/]*") + r"$")
    return any(regex.match(f) for f in files) or any(regex.match(d) for d in dirs)


def code_blocks(text):
    """The shell blocks: fenced with no language or with bash/sh. SQL and other blocks are not commands."""
    blocks, language, lines = [], None, []
    for line in text.split("\n"):
        if line.startswith("```"):
            if language is None:
                language, lines = line[3:].strip(), []
            else:
                if language in ("", "bash", "sh"):
                    blocks.append("\n".join(lines))
                language = None
        elif language is not None:
            lines.append(line)
    return blocks


def inline_commands(text):
    prose = re.sub(r"^```.*?^```", "", text, flags=re.S | re.M)
    for span in re.findall(r"`([^`\n]+)`", prose):
        first = span.split()[0] if span.split() else ""
        if first in TOOLS or first == "cd" or (first.endswith(".sh") and "/" in first):
            yield span


def commands(script):
    """The simple commands of a shell snippet, quoted strings blanked so their contents are not read as commands."""
    script = script.replace("\\\n", " ")
    script = re.sub(r"'[^'\n]*'", "''", script)
    script = re.sub(r'"(?:\\.|[^"\\\n])*"', '""', script)
    script = re.sub(r"(^|\s)#[^\n]*", r"\1", script)
    for part in re.split(r"\n|&&|\|\||;|\||\$\(|(?<![0-9>])&(?![0-9])|\(", script):
        words = part.strip().rstrip(")").split()
        while words and (re.match(r"^[A-Za-z_]+=", words[0]) or words[0] in ("do", "time")):
            words = words[1:]
        if words:
            yield words


def check_command(words, files, problems, where):
    name = words[0]
    if name.endswith(".sh"):
        if name.endswith("local_pg.sh") and len(words) > 1:
            script = open(os.path.join(ROOT, "backend/scripts/local_pg.sh")).read()
            if not re.search(r"^\s*%s\)" % re.escape(words[1]), script, re.M):
                problems.append(f"{where}: local_pg.sh has no subcommand '{words[1]}'")
        return  # the path itself is checked with the other paths
    if name in BUILTINS or name in ('""', "''") or name.startswith("-"):
        return
    if name not in TOOLS:
        problems.append(f"{where}: unknown command '{name}'")
        return
    pg = "/opt/homebrew/opt/postgresql@16/bin/"
    if not (shutil.which(name) or os.path.exists(pg + name)):
        problems.append(f"{where}: command '{name}' is not installed")
    if words[:2] == ["go", "run"] and len(words) > 2 and words[2].startswith("./cmd/"):
        main = f"backend/{words[2][2:]}/main.go"
        if main not in files:
            problems.append(f"{where}: no such program {words[2]}")
            return
        source = open(os.path.join(ROOT, main)).read()
        for flag in (w[1:] for w in words[3:] if re.match(r"^-[a-z]", w)):
            if f'flag.String("{flag}"' not in source:
                problems.append(f"{where}: {words[2]} has no flag -{flag}")


def environment_table(text):
    for line in text.split("\n"):
        if re.match(r"\| (Render|Vercel) \|", line):
            for name in re.findall(r"`([A-Z][A-Z0-9_]{2,})`", line):
                yield name


def check(name, text, files):
    dirs = {os.path.dirname(f) for f in files}
    dirs |= {"/".join(d.split("/")[:i]) for d in list(dirs) for i in range(1, len(d.split("/")) + 1)}
    problems = []
    for tok in sorted(set(path_tokens(text))):
        if not exists(tok, files, dirs):
            problems.append(f"{name}: '{tok}' is not in this branch")
    for snippet in code_blocks(text) + list(inline_commands(text)):
        for words in commands(snippet):
            check_command(words, files, problems, name)
    sources = ""
    for f in files:
        if (f.startswith("backend/") and f.endswith(".go") and not f.endswith("_test.go")) or f.startswith("frontend/src/") or f == "frontend/.env.example":
            sources += open(os.path.join(ROOT, f), errors="replace").read()
    for var in environment_table(text):
        if var not in IGNORED_ENV and not re.search(r"\b%s\b" % var, sources):
            problems.append(f"{name}: variable {var} is read by nothing in this branch")
    return problems


def self_test(files):
    fence = "```\n%s\n```\n"
    planted = {
        "a script that does not exist": "Run `backend/scripts/smoke_tenancy.sh` now.",
        "a script in a code block": fence % "backend/scripts/smoke_tenancy.sh",
        "a later migration": "Apply `backend/db/migrations/0008_event_ingestion.sql`.",
        "a verify script by bare name": "then `verify_0099.sql`",
        "a mistyped tool": fence % "pgrestore --dbname=x file",
        "a bad command after an SQL block": "```sql\nSELECT 1;\n```\n" + fence % "smoke_everything now",
        "a program that does not exist": fence % "go run ./cmd/scheduler",
        "a flag that does not exist": fence % "go run ./cmd/mkkey -tenant x",
        "a subcommand that does not exist": fence % "backend/scripts/local_pg.sh start",
        "a variable nothing reads": "| Render | `NOT_A_REAL_SETTING` | x |",
    }
    failed = [label for label, text in planted.items() if not check("planted", text, files)]
    good = fence % 'cd ~/helios-release1 && psql "service=x" -f backend/db/verify_0004.sql\ngo run ./cmd/mkkey -env dev'
    if check("good", good, files):
        failed.append("a correct snippet was reported")
    for label in failed:
        print("SELF-TEST FAILED:", label)
    if not failed:
        print(f"self-test OK: {len(planted)} planted mistakes noticed, a correct snippet accepted")
    return 1 if failed else 0


def main():
    files = tracked()
    if "--self-test" in sys.argv[1:]:
        return self_test(files)
    problems = []
    for doc in DOCS:
        text = open(os.path.join(ROOT, doc)).read()
        problems += check(doc, text, files)
        print(f"{doc}: {len(set(path_tokens(text)))} path names, {len(code_blocks(text))} code blocks, {len(list(inline_commands(text)))} inline commands")
    for p in problems:
        print("PROBLEM:", p)
    print("OK" if not problems else f"{len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
