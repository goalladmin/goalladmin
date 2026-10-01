# GoAllAdmin — AI 协作者须知 / Working Rules for AI Collaborators

> 本文件先中文、后英文，两段内容一致；**以中文为准，英文是对照译文**。改规则时两段一起改。
> Chinese first, then English; the two parts say the same thing. **The Chinese text is authoritative; the English is a translation.** Change both when changing a rule.

---

## 中文

开始任何工作前，按顺序读：

1. `docs/spec.md` —— 技术规范：范围、架构、认证、授权、表、接口、安全基线、测试、扩展与升级。代码注释里的"规范 §x.y"指它。
2. `docs/decisions.md`、`docs/conventions.md`、`docs/api.md` 和 `CHANGELOG.md`。
3. `.local/README.md`（如果存在）—— 维护者的私有工作指引，以它说的为准。

`.local/` 是私有目录，不进 git。它不存在时（例如你在开源仓库的克隆里工作），以 `README.md` 和 `docs/` 为准；要做的事由提出需求的人说明。

### 七条硬规则

1. **洁净室。** 不阅读、不复制、不改写任何非宽松许可证项目的代码来产出本项目的代码。参考只限 MIT / Apache-2.0 / BSD 项目；借用了实质性代码的文件，文件头保留出处和版权声明。本项目有自己的命名体系，CI 里的 `make banned` 会检查（词表在 `.local/banned-words.txt`）。
2. **一次只做一件事。** 当前的任务做完、相关测试全过、`make ci` 全绿，才算完成，再开下一件。
3. **不扩范围。** 规范 §2 的"不做"清单里的东西，顺手也不做；没被要求的功能不加。
4. **测试不可弱化。** 规范里的反向测试必须存在；测试失败改代码，不改断言。
5. **不引入全局变量、不用 `init()` 自注册、不用 `AutoMigrate`。** 依赖注入 + `context`；模块在 `main.go` 显式注册；表结构只通过迁移文件变更。
6. **取舍先记录再动手。** 规范没写清或你认为有错，写进 `docs/decisions.md`（日期、问题、选择、理由），然后继续。
7. **公开材料不提私有信息。** 代码、注释、提交信息、`docs/`、`CHANGELOG.md` 里不出现任何内部项目名、客户名，也不出现被参考的第三方框架名；这些只能出现在 `.local/` 下。

### 语言

文档以中文为主：`README.md` 是中文，`README.en.md` 是英文版；`CLAUDE.md`、`AGENTS.md`、`CHANGELOG.md` 在同一个文件里先中文后英文。代码标识符、接口、配置键、日志字段用英文；注释用中文。

### 环境

需要 Go 1.26+、Docker（测试用 MySQL）、Node 22+、pnpm 10+。会话开始先跑 `make ci` 确认基线是绿的。没有 Go 工具链的环境不得编写 `server/core/`。

### 完成一项工作时

汇报做了什么、改了哪些文件、`make ci` 输出的末尾，以及为验证这项工作专门跑过的命令和结果。提交信息中英双语：标题一行"英文 / 中文"，正文先英文段落、空一行再中文段落；作者是仓库 git config 里的人，正文里不加任何 AI 署名或会话链接。版本号规则见 `CHANGELOG.md` 头部。

---

## English

Before starting any work, read in this order:

1. `docs/spec.md` — the technical spec: scope, architecture, authentication, authorization, tables, API, security baseline, testing, extension and upgrades. "Spec §x.y" in code comments refers to it.
2. `docs/decisions.md`, `docs/conventions.md`, `docs/api.md` and `CHANGELOG.md`.
3. `.local/README.md`, if it exists — the maintainer's private working guide; it takes precedence.

`.local/` is a private directory and is not committed. When it is absent (for example in a clone of the open-source repository), `README.md` and `docs/` are the reference, and the person making the request says what needs to be done.

### Seven hard rules

1. **Clean room.** Never read, copy or adapt code from a project under a non-permissive licence to produce code for this project. Only MIT / Apache-2.0 / BSD projects may be used as references; a file that borrows substantial code keeps the attribution and copyright notice in its header. The project has its own naming; `make banned` in CI checks it (the word list is in `.local/banned-words.txt`).
2. **One thing at a time.** A task is done only when it is finished, its tests pass and `make ci` is green; only then start the next one.
3. **No scope creep.** Nothing from the "not doing" list in spec §2, even if it would be easy; no features that were not asked for.
4. **Tests are never weakened.** The reverse tests required by the spec must exist; when a test fails, fix the code, not the assertion.
5. **No global variables, no `init()` self-registration, no `AutoMigrate`.** Dependency injection plus `context`; modules are registered explicitly in `main.go`; schema changes only through migration files.
6. **Record trade-offs before acting.** When the spec is unclear or you believe it is wrong, write an entry in `docs/decisions.md` (date, problem, choice, reason), then continue.
7. **No private information in public material.** Code, comments, commit messages, `docs/` and `CHANGELOG.md` never mention internal project names, customer names or the names of third-party frameworks used as references; those belong only under `.local/`.

### Language

Documentation is Chinese first: `README.md` is Chinese and `README.en.md` is the English edition; `CLAUDE.md`, `AGENTS.md` and `CHANGELOG.md` carry Chinese followed by English in the same file. Code identifiers, the API, configuration keys and log fields are in English; comments are in Chinese.

### Environment

Go 1.26+, Docker (MySQL for tests), Node 22+ and pnpm 10+. Run `make ci` at the start of a session to confirm the baseline is green. Do not write `server/core/` in an environment without a Go toolchain.

### When a piece of work is done

Report what was done, which files changed, the tail of the `make ci` output, and the commands run specifically to verify this work along with their results. Commit messages are bilingual: a one-line subject "English / 中文", then an English paragraph, a blank line and a Chinese paragraph; the author is the person in the repository's git config, with no AI attribution or session links in the body. The versioning rule is at the top of `CHANGELOG.md`.
