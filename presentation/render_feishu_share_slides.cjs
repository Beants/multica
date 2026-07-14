const fs = require("node:fs/promises");
const path = require("node:path");
const { pathToFileURL } = require("node:url");

const ROOT = __dirname;
const ASSETS_DIR = path.join(ROOT, "assets");
const HTML_OUTPUT = path.join(ROOT, "feishu-share-slides-05-08.html");
const SLIDE_WIDTH = 1672;
const SLIDE_HEIGHT = 941;

function resolvePackage(pkgName) {
  try {
    return require(pkgName);
  } catch (error) {
    const extraModulesDir = process.env.CODEX_NODE_MODULES;
    if (!extraModulesDir) {
      throw error;
    }
    return require(path.join(extraModulesDir, pkgName));
  }
}

const { chromium } = resolvePackage("playwright");
const lucide = resolvePackage("lucide");

function renderLucideIcon(name, size = 34, color = "#1654e9") {
  const iconNode = lucide[name];
  if (!iconNode) {
    return `<div class="icon-fallback">${name.slice(0, 1)}</div>`;
  }

  const children = iconNode
    .map(([tag, attrs]) => {
      const attrString = Object.entries(attrs)
        .map(([key, value]) => `${key}="${String(value)}"`)
        .join(" ");
      return `<${tag} ${attrString}></${tag}>`;
    })
    .join("");

  return [
    `<svg class="icon-svg" viewBox="0 0 24 24" width="${size}" height="${size}"`,
    ` fill="none" stroke="${color}" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"`,
    ` aria-hidden="true">${children}</svg>`,
  ].join("");
}

function iconBubble(name, options = {}) {
  const {
    size = "md",
    tone = "blue",
    ring = false,
    label = "",
  } = options;
  return `
    <div class="icon-bubble icon-${size} tone-${tone}${ring ? " icon-ring" : ""}">
      ${renderLucideIcon(name, size === "lg" ? 40 : size === "sm" ? 28 : 34)}
      ${label ? `<span class="icon-label">${label}</span>` : ""}
    </div>
  `;
}

function topChrome(number) {
  return `
    <div class="slide-meta">
      <div class="meta-line"></div>
      <div class="page-badge">${number}</div>
    </div>
  `;
}

function bottomBand() {
  return `
    <div class="bottom-band">
      <div class="bottom-band-main"></div>
      <div class="bottom-band-tail"></div>
    </div>
  `;
}

function featureChip(iconName, title, desc) {
  return `
    <div class="feature-chip">
      ${iconBubble(iconName, { size: "sm", tone: "solid" })}
      <div>
        <div class="feature-chip-title">${title}</div>
        <div class="feature-chip-desc">${desc}</div>
      </div>
    </div>
  `;
}

function slide05() {
  return `
    <section class="slide" id="slide-05">
      ${topChrome("05")}
      <div class="slide-grid"></div>
      <div class="halo halo-right"></div>
      <div class="dots dots-right"></div>
      <header class="header-block">
        <div class="kicker">五、联合工作流</div>
        <h1>从 Spec 到验证结果的闭环</h1>
        <p class="subtitle">重点不是“让 Agent 自由发挥”，而是把输入、执行、审查和沉淀组织成一条稳定闭环。</p>
      </header>

      <div class="flow-board">
        <div class="flow-step">
          <div class="step-index">1</div>
          ${iconBubble("FileText", { tone: "blue" })}
          <div class="step-title">Spec</div>
          <div class="step-desc">问题、指标、Non-Goals</div>
        </div>
        <div class="flow-arrow"></div>
        <div class="flow-step">
          <div class="step-index">2</div>
          ${iconBubble("ClipboardList", { tone: "blue" })}
          <div class="step-title">Plan</div>
          <div class="step-desc">实现路径与验收动作</div>
        </div>
        <div class="flow-arrow"></div>
        <div class="flow-step">
          <div class="step-index">3</div>
          ${iconBubble("KanbanSquare", { tone: "blue" })}
          <div class="step-title">Issue 调度</div>
          <div class="step-desc">Multica 分配角色与状态</div>
        </div>
        <div class="flow-arrow"></div>
        <div class="flow-step step-wide">
          <div class="step-index">4</div>
          ${iconBubble("GitPullRequestArrow", { tone: "blue" })}
          <div class="step-title">执行与审查</div>
          <div class="step-desc">Coder 实现，Reviewer 独立把关</div>
          <div class="step-loop">
            <span>超过 2 轮仍未收敛</span>
            <strong>升级到人决策</strong>
          </div>
        </div>
        <div class="flow-arrow"></div>
        <div class="flow-step">
          <div class="step-index">5</div>
          ${iconBubble("BookMarked", { tone: "blue" })}
          <div class="step-title">沉淀复用</div>
          <div class="step-desc">LongRunner / Finish 回写规范</div>
        </div>
      </div>

      <div class="case-row">
        <div class="case-card">
          <div class="case-topline">
            <div>
              <div class="case-tag">快乐路径</div>
              <h3>BEA-10 评测集 + 自动化测试</h3>
            </div>
            <div class="case-duration">28 分钟</div>
          </div>
          <div class="timeline">
            <div class="timeline-step"><span>Master</span><small>assign 给 Coder</small></div>
            <div class="timeline-join"></div>
            <div class="timeline-step"><span>Coder</span><small>29 条用例 + 5 个测试文件</small></div>
            <div class="timeline-join"></div>
            <div class="timeline-step"><span>Reviewer</span><small>修 lint 后 GREEN</small></div>
            <div class="timeline-join"></div>
            <div class="timeline-step success"><span>Close</span><small>关闭 BEA-10</small></div>
          </div>
          <div class="case-note">
            <span class="note-label">踩坑</span>
            审查通过后必须合入主分支，否则代码容易停留在 reviewer 分支。
          </div>
        </div>

        <div class="case-card accent-card">
          <div class="case-topline">
            <div>
              <div class="case-tag">复杂路径</div>
              <h3>BEA-11 Prompt 调优 + 异常兜底</h3>
            </div>
            <div class="case-duration">65 分钟</div>
          </div>
          <div class="review-cycle">
            <div class="cycle-pill red">Round 1 Review: RED</div>
            <div class="cycle-arrow"></div>
            <div class="cycle-pill">Fix</div>
            <div class="cycle-arrow"></div>
            <div class="cycle-pill yellow">Round 2 Review: YELLOW</div>
            <div class="cycle-arrow"></div>
            <div class="cycle-pill emphasis">人做方案选择</div>
            <div class="cycle-arrow"></div>
            <div class="cycle-pill green">Round 3: GREEN</div>
          </div>
          <div class="case-note">
            <span class="note-label">关键动作</span>
            触发“评审最多 2 轮”的上限后，Master 自动升级到人，避免无限打回。
          </div>
        </div>
      </div>

      <div class="feature-row feature-row-tight">
        ${featureChip("Database", "输入结构化", "Spec / Plan / Issue 作为统一入口")}
        ${featureChip("Eye", "过程可观察", "评论、状态和 session 让进度持续可见")}
        ${featureChip("ShieldCheck", "结果可验证", "执行与审查分离，结论更可信")}
      </div>
      ${bottomBand()}
    </section>
  `;
}

function roleCard(title, subtitle, iconName, owned, notOwned, extraClass = "") {
  return `
    <div class="role-card ${extraClass}">
      <div class="role-card-top">
        ${iconBubble(iconName, { tone: "blue" })}
        <div>
          <div class="role-title">${title}</div>
          <div class="role-subtitle">${subtitle}</div>
        </div>
      </div>
      <div class="role-divider"></div>
      <div class="role-duty">
        <span>负责</span>
        <p>${owned}</p>
      </div>
      <div class="role-duty muted-duty">
        <span>不负责</span>
        <p>${notOwned}</p>
      </div>
    </div>
  `;
}

function slide06() {
  return `
    <section class="slide" id="slide-06">
      ${topChrome("06")}
      <div class="slide-grid"></div>
      <div class="halo halo-center"></div>
      <div class="dots dots-top-right"></div>
      <header class="header-block">
        <div class="kicker">五、联合工作流</div>
        <h1>角色分工与执行隔离</h1>
        <p class="subtitle">多 Agent 不是堆更多模型，而是让不同角色只做自己最该做的那一段工作。</p>
      </header>

      <div class="master-lane">
        <div class="master-card">
          ${iconBubble("Waypoints", { size: "lg", tone: "solid" })}
          <div>
            <div class="master-title">Master Orchestrator</div>
            <div class="master-desc">负责任务路由、状态收口和异常升级，不直接写业务代码。</div>
          </div>
        </div>
        <div class="dispatch-links">
          <div class="dispatch-link"></div>
          <div class="dispatch-link"></div>
          <div class="dispatch-link"></div>
        </div>
      </div>

      <div class="separation-stamp">
        <div class="stamp-title">执行与评估分离</div>
        <div class="stamp-desc">Coder 不给自己结论，Reviewer 不承担实现职责</div>
      </div>

      <div class="roles-grid">
        ${roleCard(
          "Coder",
          "代码实现 / 跑测试 / 快速修复",
          "Code2",
          "实现代码、补测试、响应 Review。",
          "不能自己宣布“已经没问题”。"
        )}
        ${roleCard(
          "Reviewer",
          "代码审查 / 测试审查 / 质量门禁",
          "SearchCode",
          "独立找风险、验证回归、给 RED/YELLOW/GREEN。",
          "不背实现指标，也不顺手接管编码。"
        )}
        ${roleCard(
          "LongRunner",
          "长任务 / 调研 / 中文文档输出",
          "BookOpenText",
          "处理长周期任务，提取经验，补充文档。",
          "不承担高频短链路的即时实现。"
        )}
      </div>

      <div class="isolation-panel">
        <div class="isolation-column">
          <div class="mini-label">为什么要拆角色</div>
          <div class="isolation-list">
            <div class="isolation-item">${renderLucideIcon("Split", 18)} 避免一个 Agent 同时兼顾协调、实现、审查。</div>
            <div class="isolation-item">${renderLucideIcon("Shield", 18)} 防止自我确认偏差把问题直接带进主线。</div>
            <div class="isolation-item">${renderLucideIcon("Clock3", 18)} 让长任务从高频链路里分流出去。</div>
          </div>
        </div>
        <div class="isolation-column right-column">
          <div class="mini-label">工程化收益</div>
          <div class="benefit-grid">
            ${featureChip("UsersRound", "职责清晰", "谁推进、谁把关、谁收口一目了然")}
            ${featureChip("BadgeCheck", "独立审查", "评估结论来自另一个角色而不是作者本人")}
            ${featureChip("Route", "任务分流", "长任务与短链路任务可以并行而不打架")}
          </div>
        </div>
      </div>
      ${bottomBand()}
    </section>
  `;
}

function pitfallCard(index, title, rule, iconName) {
  return `
    <div class="pitfall-card">
      <div class="pitfall-index">坑 ${index}</div>
      <div class="pitfall-main">
        ${iconBubble(iconName, { size: "sm", tone: "blue" })}
        <div>
          <div class="pitfall-title">${title}</div>
          <div class="pitfall-rule">${rule}</div>
        </div>
      </div>
    </div>
  `;
}

function loopNode(title, iconName, className = "") {
  return `
    <div class="loop-node ${className}">
      ${iconBubble(iconName, { size: "sm", tone: "blue" })}
      <span>${title}</span>
    </div>
  `;
}

function slide07() {
  return `
    <section class="slide" id="slide-07">
      ${topChrome("07")}
      <div class="slide-grid"></div>
      <div class="halo halo-right"></div>
      <header class="header-block">
        <div class="kicker">六、实践经验</div>
        <h1>五个踩过的坑，如何长成规则</h1>
        <p class="subtitle">真正有效的提示词不是先设计好的，而是从真实运行问题里一轮轮跑出来的。</p>
      </header>

      <div class="pitfall-layout">
        <div class="pitfall-stack">
          ${pitfallCard("1", "Agent 记忆不可靠", "同一 issue 跨 run 容易丢上下文，所以补了 PROGRESS.md 和文件化交接。", "Brain")}
          ${pitfallCard("2", "Agent 自己审查自己", "Coder / Reviewer 强制分离，执行者不能给自己最终结论。", "ShieldCheck")}
          ${pitfallCard("3", "流程卡死没人管", "评论 @Master + assign 回 Master，两步汇报缺一不可。", "Route")}
          ${pitfallCard("4", "迭代失控", "代码/测试评审最多 2 轮，超过就自动升级到人。", "RefreshCcwDot")}
          ${pitfallCard("5", "Runtime 挂了 + 429 限速", "设定延时重试和次数上限，超过阈值再升级处理。", "Gauge")}
        </div>

        <div class="loop-panel">
          <div class="loop-title">提示词优化闭环</div>
          <div class="loop-graph">
            <div class="loop-core">
              <strong>持续改进</strong>
              <span>不是“设计”，而是“运行后修订”</span>
            </div>
            ${loopNode("运行任务", "Play")}
            ${loopNode("记录日志", "NotebookPen", "node-top")}
            ${loopNode("分析失败", "ChartNoAxesColumn", "node-right")}
            ${loopNode("修订规则", "FilePenLine", "node-bottom")}
            ${loopNode("重跑验证", "CheckCheck", "node-left")}
            <div class="loop-ring ring-outer"></div>
            <div class="loop-ring ring-inner"></div>
            <div class="loop-arc arc-1"></div>
            <div class="loop-arc arc-2"></div>
            <div class="loop-arc arc-3"></div>
            <div class="loop-arc arc-4"></div>
          </div>
          <div class="loop-footer">
            <div class="loop-quote">“提示词不是设计出来的，是跑出来的。”</div>
            <div class="loop-subquote">阿里 / 淘宝 / 美团等经验总结提供基础版本，真实项目运行决定最后的硬规则。</div>
          </div>
        </div>
      </div>

      <div class="feature-row feature-row-tight">
        ${featureChip("FileStack", "文件化上下文", "Issue、jsonl、PROGRESS.md 共同承接跨 run 信息")}
        ${featureChip("Scale", "执行评估分离", "质量判断必须来自独立角色而不是作者本人")}
        ${featureChip("AlarmClockCheck", "上限与重试", "给迭代次数、重试次数和升级阈值设硬边界")}
      </div>
      ${bottomBand()}
    </section>
  `;
}

function summaryCard(title, desc, iconName, detail) {
  return `
    <div class="summary-card">
      ${iconBubble(iconName, { size: "lg", tone: "blue" })}
      <div class="summary-card-title">${title}</div>
      <div class="summary-card-desc">${desc}</div>
      <div class="summary-card-detail">${detail}</div>
    </div>
  `;
}

function actionPill(text) {
  return `<div class="action-pill">${text}</div>`;
}

function slide08() {
  return `
    <section class="slide" id="slide-08">
      ${topChrome("08")}
      <div class="slide-grid"></div>
      <div class="halo halo-center"></div>
      <div class="dots dots-right"></div>
      <header class="header-block center-header">
        <div class="kicker">七、结论</div>
        <h1>稳定性来自协作机制，不来自单一模型</h1>
        <p class="subtitle">把工具、流程和角色拆开，AI 协作开发才会真正进入可复用、可验证、可管理的工程状态。</p>
      </header>

      <div class="summary-row">
        ${summaryCard("Multica", "多 Agent 协作平台", "Cloud", "统一 Issue、角色、状态与本地 Runtime 调度。")}
        ${summaryCard("Trellis", "单 Agent 执行框架", "Blocks", "把规范、任务上下文、工作流和日志拆成按需加载的结构。")}
        ${summaryCard("SDD", "结构化任务定义", "ClipboardCheck", "用 Spec / Plan 固定 WHAT，再把 HOW 交给 Agent 去实现。")}
      </div>

      <div class="conclusion-rail">
        <div class="rail-line left"></div>
        <div class="rail-node"></div>
        <div class="conclusion-banner">
          <div class="conclusion-title">一条更稳的工程路径</div>
          <div class="conclusion-path">Spec 约束目标 → Plan 约束实施 → 平台组织协作 → 角色分离控制质量 → 复盘沉淀规范</div>
        </div>
        <div class="rail-node"></div>
        <div class="rail-line right"></div>
      </div>

      <div class="priority-panel">
        <div class="priority-title">建议优先落地</div>
        <div class="priority-row">
          ${actionPill("先建立 Spec / Plan 习惯")}
          ${actionPill("明确 Coder / Reviewer 分离")}
          ${actionPill("将关键上下文文件化")}
          ${actionPill("再引入平台化调度")}
        </div>
      </div>

      <div class="closing-note">
        <div class="closing-line"></div>
        <div class="closing-text">谢谢，欢迎交流</div>
        <div class="closing-line"></div>
      </div>
      ${bottomBand()}
    </section>
  `;
}

function buildHtml() {
  return `
    <!doctype html>
    <html lang="zh-CN">
      <head>
        <meta charset="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <title>Feishu Share Slides 05-08</title>
        <style>
          :root {
            --bg: #f6f9ff;
            --surface: rgba(255, 255, 255, 0.92);
            --surface-strong: #ffffff;
            --line: rgba(63, 112, 220, 0.18);
            --line-strong: rgba(32, 86, 220, 0.28);
            --text: #0b245d;
            --muted: #5b6886;
            --primary: #1654e9;
            --primary-strong: #0f43c2;
            --primary-soft: #eaf1ff;
            --success: #18a957;
            --warning: #e0a400;
            --danger: #d74b5a;
            --shadow: 0 22px 52px rgba(17, 52, 124, 0.12);
            --radius-xl: 28px;
            --radius-lg: 22px;
            --radius-md: 18px;
            --radius-sm: 14px;
          }

          * { box-sizing: border-box; }

          html, body {
            margin: 0;
            padding: 0;
            background: #edf3ff;
            color: var(--text);
            font-family: "SF Pro Display", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
          }

          body {
            padding: 24px 0 48px;
          }

          .deck {
            width: ${SLIDE_WIDTH}px;
            margin: 0 auto;
            display: grid;
            gap: 32px;
          }

          .slide {
            position: relative;
            width: ${SLIDE_WIDTH}px;
            height: ${SLIDE_HEIGHT}px;
            overflow: hidden;
            background:
              radial-gradient(circle at 78% 18%, rgba(58, 115, 255, 0.1), transparent 27%),
              radial-gradient(circle at 12% 86%, rgba(58, 115, 255, 0.08), transparent 24%),
              linear-gradient(180deg, #ffffff 0%, #fbfdff 100%);
            border-radius: 0;
            box-shadow: 0 28px 90px rgba(15, 43, 112, 0.12);
          }

          .slide-grid {
            position: absolute;
            inset: 0;
            background-image:
              linear-gradient(rgba(34, 92, 221, 0.035) 1px, transparent 1px),
              linear-gradient(90deg, rgba(34, 92, 221, 0.035) 1px, transparent 1px);
            background-size: 28px 28px;
            opacity: 0.45;
            mask-image: linear-gradient(180deg, rgba(0,0,0,0.4), rgba(0,0,0,0.08));
          }

          .halo {
            position: absolute;
            width: 700px;
            height: 700px;
            border-radius: 50%;
            border: 1px solid rgba(72, 115, 224, 0.16);
            opacity: 0.7;
          }

          .halo::before,
          .halo::after {
            content: "";
            position: absolute;
            inset: 52px;
            border-radius: 50%;
            border: 1px solid rgba(72, 115, 224, 0.12);
          }

          .halo::after {
            inset: 120px;
          }

          .halo-right { right: -80px; top: -72px; }
          .halo-center { right: 110px; top: 84px; transform: scale(0.88); opacity: 0.42; }

          .dots {
            position: absolute;
            width: 120px;
            height: 120px;
            background-image: radial-gradient(circle, rgba(36, 89, 218, 0.45) 1.5px, transparent 1.5px);
            background-size: 18px 18px;
            opacity: 0.65;
          }

          .dots-right { right: 58px; bottom: 42px; }
          .dots-top-right { right: 58px; top: 56px; width: 92px; height: 92px; }

          .slide-meta {
            position: absolute;
            top: 46px;
            left: 58px;
            right: 58px;
            display: flex;
            align-items: center;
            justify-content: space-between;
            z-index: 2;
          }

          .meta-line {
            width: 248px;
            height: 4px;
            border-radius: 999px;
            background: linear-gradient(90deg, #0f43c2 0 34%, rgba(15, 67, 194, 0.16) 34% 100%);
          }

          .page-badge {
            min-width: 72px;
            padding: 10px 16px;
            border-radius: 18px;
            background: linear-gradient(180deg, #1248d3, #0f43c2);
            color: #fff;
            text-align: center;
            font-weight: 800;
            font-size: 34px;
            letter-spacing: 0.04em;
            box-shadow: 0 10px 24px rgba(17, 65, 182, 0.22);
          }

          .header-block {
            position: relative;
            z-index: 2;
            padding: 118px 84px 0;
          }

          .center-header {
            text-align: center;
          }

          .kicker {
            display: inline-flex;
            align-items: center;
            gap: 10px;
            color: var(--primary);
            font-size: 21px;
            font-weight: 800;
            letter-spacing: 0.06em;
            text-transform: uppercase;
          }

          .kicker::before {
            content: "";
            width: 46px;
            height: 4px;
            border-radius: 999px;
            background: var(--primary);
          }

          h1 {
            margin: 18px 0 14px;
            font-size: 68px;
            line-height: 1.08;
            letter-spacing: -0.03em;
          }

          .subtitle {
            margin: 0;
            max-width: 1160px;
            color: var(--muted);
            font-size: 28px;
            line-height: 1.5;
          }

          .icon-bubble {
            position: relative;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            border-radius: 50%;
            background: linear-gradient(180deg, rgba(23, 86, 230, 0.1), rgba(23, 86, 230, 0.04));
            border: 1px solid rgba(23, 86, 230, 0.1);
            flex-shrink: 0;
          }

          .icon-md { width: 84px; height: 84px; }
          .icon-lg { width: 94px; height: 94px; }
          .icon-sm { width: 64px; height: 64px; }
          .tone-solid {
            background: linear-gradient(180deg, #1a58ea, #1147c6);
            border-color: transparent;
            box-shadow: 0 12px 26px rgba(17, 72, 211, 0.22);
          }

          .tone-solid .icon-svg {
            stroke: #fff !important;
          }

          .icon-ring {
            box-shadow: inset 0 0 0 10px rgba(23, 86, 230, 0.06);
          }

          .icon-fallback {
            font-size: 28px;
            font-weight: 800;
            color: var(--primary);
          }

          .flow-board {
            position: relative;
            z-index: 2;
            margin: 34px 84px 0;
            padding: 28px 30px;
            display: grid;
            grid-template-columns: 1fr 38px 1fr 38px 1fr 38px 1.3fr 38px 1fr;
            align-items: center;
            background: linear-gradient(180deg, rgba(255,255,255,0.94), rgba(255,255,255,0.8));
            border: 1px solid var(--line);
            border-radius: var(--radius-xl);
            box-shadow: var(--shadow);
          }

          .flow-step {
            min-height: 212px;
            padding: 22px 18px 20px;
            border-radius: var(--radius-lg);
            border: 1px solid rgba(36, 89, 218, 0.12);
            background: linear-gradient(180deg, rgba(255,255,255,0.96), rgba(248,251,255,0.95));
            display: flex;
            flex-direction: column;
            align-items: center;
            text-align: center;
            gap: 12px;
            position: relative;
          }

          .step-wide {
            padding-left: 20px;
            padding-right: 20px;
          }

          .step-index {
            position: absolute;
            left: 16px;
            top: 16px;
            width: 36px;
            height: 36px;
            border-radius: 50%;
            background: linear-gradient(180deg, #1a58ea, #1047c7);
            color: #fff;
            font-size: 18px;
            font-weight: 800;
            display: grid;
            place-items: center;
            box-shadow: 0 10px 18px rgba(18, 71, 199, 0.22);
          }

          .step-title {
            font-size: 28px;
            font-weight: 800;
            letter-spacing: -0.02em;
          }

          .step-desc {
            color: var(--muted);
            font-size: 18px;
            line-height: 1.45;
          }

          .step-loop {
            margin-top: auto;
            width: 100%;
            padding: 12px 14px;
            border-radius: 16px;
            border: 1px dashed rgba(215, 75, 90, 0.32);
            background: rgba(255, 244, 246, 0.92);
            color: #8f2742;
            font-size: 15px;
            display: grid;
            gap: 4px;
          }

          .step-loop strong {
            font-size: 18px;
          }

          .flow-arrow {
            position: relative;
            height: 4px;
            border-radius: 999px;
            background: linear-gradient(90deg, rgba(22,84,233,0.28), rgba(22,84,233,0.85));
          }

          .flow-arrow::after {
            content: "";
            position: absolute;
            right: -2px;
            top: -7px;
            width: 18px;
            height: 18px;
            border-top: 4px solid var(--primary);
            border-right: 4px solid var(--primary);
            transform: rotate(45deg);
          }

          .case-row {
            position: relative;
            z-index: 2;
            margin: 28px 84px 0;
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 24px;
          }

          .case-card {
            padding: 22px 24px 22px;
            background: linear-gradient(180deg, rgba(255,255,255,0.96), rgba(247,250,255,0.94));
            border-radius: var(--radius-lg);
            border: 1px solid var(--line);
            box-shadow: var(--shadow);
          }

          .accent-card {
            background: linear-gradient(180deg, rgba(246,250,255,0.98), rgba(242,247,255,0.96));
          }

          .case-topline {
            display: flex;
            align-items: flex-start;
            justify-content: space-between;
            gap: 16px;
            margin-bottom: 18px;
          }

          .case-topline h3 {
            margin: 8px 0 0;
            font-size: 28px;
            line-height: 1.2;
          }

          .case-tag,
          .mini-label {
            display: inline-flex;
            align-items: center;
            padding: 7px 12px;
            border-radius: 999px;
            background: rgba(22, 84, 233, 0.1);
            color: var(--primary);
            font-size: 15px;
            font-weight: 800;
            letter-spacing: 0.04em;
          }

          .case-duration {
            padding: 9px 14px;
            border-radius: 999px;
            background: rgba(11, 36, 93, 0.06);
            font-size: 16px;
            font-weight: 700;
            white-space: nowrap;
          }

          .timeline {
            display: grid;
            grid-template-columns: 1fr 28px 1fr 28px 1fr 28px 1fr;
            align-items: center;
            gap: 0;
          }

          .timeline-step {
            min-height: 112px;
            padding: 16px 14px;
            border-radius: 18px;
            border: 1px solid rgba(22, 84, 233, 0.12);
            background: rgba(255,255,255,0.94);
            display: grid;
            align-content: center;
            gap: 8px;
            text-align: center;
          }

          .timeline-step span {
            font-size: 20px;
            font-weight: 800;
          }

          .timeline-step small {
            font-size: 15px;
            line-height: 1.45;
            color: var(--muted);
          }

          .timeline-step.success {
            background: linear-gradient(180deg, rgba(24, 169, 87, 0.12), rgba(255,255,255,0.94));
            border-color: rgba(24, 169, 87, 0.26);
          }

          .timeline-join {
            position: relative;
            height: 4px;
            border-radius: 999px;
            background: linear-gradient(90deg, rgba(22,84,233,0.18), rgba(22,84,233,0.84));
          }

          .timeline-join::after,
          .cycle-arrow::after {
            content: "";
            position: absolute;
            right: -1px;
            top: -6px;
            width: 16px;
            height: 16px;
            border-top: 3px solid var(--primary);
            border-right: 3px solid var(--primary);
            transform: rotate(45deg);
          }

          .case-note {
            margin-top: 16px;
            padding: 14px 16px;
            border-radius: 16px;
            background: rgba(15, 67, 194, 0.06);
            color: var(--text);
            font-size: 16px;
            line-height: 1.55;
          }

          .note-label {
            display: inline-block;
            margin-right: 8px;
            color: var(--primary-strong);
            font-weight: 800;
          }

          .review-cycle {
            display: grid;
            grid-template-columns: 1.35fr 20px .72fr 20px 1.45fr 20px 1.15fr 20px 1fr;
            align-items: center;
          }

          .cycle-pill {
            min-height: 84px;
            padding: 14px 12px;
            border-radius: 18px;
            border: 1px solid rgba(22,84,233,0.14);
            background: rgba(255,255,255,0.94);
            font-size: 16px;
            line-height: 1.45;
            font-weight: 700;
            text-align: center;
            display: grid;
            place-items: center;
          }

          .cycle-pill.red {
            background: rgba(255, 245, 247, 0.98);
            border-color: rgba(215, 75, 90, 0.24);
            color: #9b2642;
          }

          .cycle-pill.yellow {
            background: rgba(255, 250, 236, 0.98);
            border-color: rgba(224, 164, 0, 0.26);
            color: #926d00;
          }

          .cycle-pill.green {
            background: rgba(241, 252, 246, 0.98);
            border-color: rgba(24, 169, 87, 0.28);
            color: #127c46;
          }

          .cycle-pill.emphasis {
            background: linear-gradient(180deg, rgba(22,84,233,0.14), rgba(255,255,255,0.98));
            border-color: rgba(22,84,233,0.24);
          }

          .cycle-arrow {
            position: relative;
            height: 3px;
            background: rgba(22,84,233,0.72);
          }

          .feature-row {
            position: relative;
            z-index: 2;
            margin: 24px 84px 0;
            display: grid;
            grid-template-columns: repeat(3, 1fr);
            gap: 16px;
          }

          .feature-row-tight {
            margin-top: 22px;
          }

          .feature-chip {
            display: flex;
            align-items: center;
            gap: 16px;
            padding: 18px 20px;
            border-radius: 20px;
            border: 1px solid var(--line);
            background: rgba(255,255,255,0.88);
            box-shadow: 0 16px 36px rgba(15, 43, 112, 0.08);
          }

          .feature-chip-title {
            font-size: 22px;
            font-weight: 800;
            margin-bottom: 4px;
          }

          .feature-chip-desc {
            font-size: 16px;
            line-height: 1.5;
            color: var(--muted);
          }

          .master-lane {
            position: relative;
            z-index: 2;
            margin: 34px 84px 0;
          }

          .master-card {
            padding: 24px 28px;
            border-radius: 24px;
            background: linear-gradient(180deg, rgba(250,252,255,0.98), rgba(244,249,255,0.96));
            border: 1px solid var(--line);
            box-shadow: var(--shadow);
            display: flex;
            align-items: center;
            gap: 20px;
            width: 760px;
          }

          .master-title {
            font-size: 34px;
            font-weight: 800;
            margin-bottom: 6px;
          }

          .master-desc {
            color: var(--muted);
            font-size: 19px;
            line-height: 1.5;
          }

          .dispatch-links {
            display: grid;
            grid-template-columns: repeat(3, 1fr);
            gap: 26px;
            padding: 20px 92px 0;
            width: 100%;
          }

          .dispatch-link {
            position: relative;
            height: 52px;
          }

          .dispatch-link::before {
            content: "";
            position: absolute;
            left: 50%;
            top: 0;
            width: 4px;
            height: 28px;
            border-radius: 999px;
            background: linear-gradient(180deg, rgba(22,84,233,0.92), rgba(22,84,233,0.18));
            transform: translateX(-50%);
          }

          .dispatch-link::after {
            content: "";
            position: absolute;
            left: calc(50% - 8px);
            bottom: 0;
            width: 16px;
            height: 16px;
            border-right: 4px solid var(--primary);
            border-bottom: 4px solid var(--primary);
            transform: rotate(45deg);
          }

          .separation-stamp {
            position: absolute;
            z-index: 3;
            right: 86px;
            top: 264px;
            width: 510px;
            padding: 22px 24px;
            border-radius: 22px;
            background: linear-gradient(180deg, rgba(14, 67, 194, 0.95), rgba(21, 84, 233, 0.88));
            color: #fff;
            box-shadow: 0 24px 50px rgba(13, 55, 161, 0.26);
          }

          .stamp-title {
            font-size: 30px;
            font-weight: 900;
            margin-bottom: 8px;
          }

          .stamp-desc {
            font-size: 18px;
            line-height: 1.45;
            opacity: 0.92;
          }

          .roles-grid {
            position: relative;
            z-index: 2;
            margin: 10px 84px 0;
            display: grid;
            grid-template-columns: repeat(3, 1fr);
            gap: 24px;
          }

          .role-card {
            min-height: 286px;
            padding: 22px 22px 20px;
            border-radius: 24px;
            background: linear-gradient(180deg, rgba(255,255,255,0.98), rgba(247,250,255,0.96));
            border: 1px solid var(--line);
            box-shadow: var(--shadow);
          }

          .role-card-top {
            display: flex;
            align-items: center;
            gap: 16px;
          }

          .role-title {
            font-size: 30px;
            font-weight: 800;
            margin-bottom: 4px;
          }

          .role-subtitle {
            color: var(--muted);
            font-size: 16px;
            line-height: 1.45;
          }

          .role-divider {
            margin: 18px 0 16px;
            height: 1px;
            background: linear-gradient(90deg, rgba(22,84,233,0.2), rgba(22,84,233,0.06));
          }

          .role-duty {
            display: grid;
            gap: 8px;
            margin-bottom: 12px;
          }

          .role-duty span {
            color: var(--primary-strong);
            font-size: 14px;
            font-weight: 800;
            letter-spacing: 0.08em;
          }

          .role-duty p {
            margin: 0;
            font-size: 18px;
            line-height: 1.55;
          }

          .muted-duty {
            color: var(--muted);
          }

          .muted-duty span {
            color: #7b87a3;
          }

          .isolation-panel {
            position: relative;
            z-index: 2;
            margin: 20px 84px 0;
            display: grid;
            grid-template-columns: 1.08fr 1fr;
            gap: 24px;
          }

          .isolation-column {
            padding: 22px 24px;
            border-radius: 24px;
            border: 1px solid var(--line);
            background: rgba(255,255,255,0.9);
            box-shadow: 0 18px 42px rgba(15, 43, 112, 0.08);
          }

          .isolation-list {
            display: grid;
            gap: 14px;
            margin-top: 16px;
          }

          .isolation-item {
            display: flex;
            align-items: flex-start;
            gap: 10px;
            font-size: 18px;
            line-height: 1.55;
            color: var(--text);
          }

          .benefit-grid {
            display: grid;
            gap: 14px;
            margin-top: 16px;
          }

          .pitfall-layout {
            position: relative;
            z-index: 2;
            margin: 32px 84px 0;
            display: grid;
            grid-template-columns: 1.02fr 0.98fr;
            gap: 28px;
          }

          .pitfall-stack {
            display: grid;
            gap: 16px;
          }

          .pitfall-card {
            padding: 18px 20px 18px;
            border-radius: 22px;
            border: 1px solid var(--line);
            background: linear-gradient(180deg, rgba(255,255,255,0.97), rgba(247,250,255,0.95));
            box-shadow: 0 18px 42px rgba(15, 43, 112, 0.08);
          }

          .pitfall-index {
            display: inline-flex;
            padding: 6px 10px;
            border-radius: 999px;
            background: rgba(22,84,233,0.1);
            color: var(--primary);
            font-size: 14px;
            font-weight: 800;
            letter-spacing: 0.06em;
          }

          .pitfall-main {
            display: flex;
            align-items: center;
            gap: 14px;
            margin-top: 12px;
          }

          .pitfall-title {
            font-size: 24px;
            font-weight: 800;
            margin-bottom: 6px;
          }

          .pitfall-rule {
            color: var(--muted);
            font-size: 17px;
            line-height: 1.55;
          }

          .loop-panel {
            padding: 22px 24px 20px;
            border-radius: 26px;
            border: 1px solid var(--line);
            background: linear-gradient(180deg, rgba(255,255,255,0.98), rgba(245,249,255,0.96));
            box-shadow: var(--shadow);
          }

          .loop-title {
            font-size: 28px;
            font-weight: 800;
          }

          .loop-graph {
            position: relative;
            height: 432px;
            margin-top: 10px;
          }

          .loop-core {
            position: absolute;
            left: 50%;
            top: 50%;
            width: 246px;
            height: 246px;
            transform: translate(-50%, -50%);
            border-radius: 50%;
            background: radial-gradient(circle at 50% 36%, rgba(255,255,255,0.98), rgba(230,239,255,0.92));
            border: 1px solid rgba(22,84,233,0.14);
            display: grid;
            place-items: center;
            text-align: center;
            padding: 34px;
            box-shadow: inset 0 0 0 18px rgba(22,84,233,0.04);
          }

          .loop-core strong {
            display: block;
            font-size: 34px;
            margin-bottom: 10px;
          }

          .loop-core span {
            color: var(--muted);
            font-size: 18px;
            line-height: 1.5;
          }

          .loop-node {
            position: absolute;
            left: 50%;
            top: 30px;
            transform: translateX(-50%);
            width: 168px;
            padding: 14px 10px 16px;
            border-radius: 24px;
            background: rgba(255,255,255,0.96);
            border: 1px solid rgba(22,84,233,0.14);
            display: grid;
            justify-items: center;
            gap: 10px;
            font-size: 18px;
            font-weight: 700;
            box-shadow: 0 14px 32px rgba(15, 43, 112, 0.08);
          }

          .node-top { top: 0; }
          .node-right { left: auto; right: 18px; top: 50%; transform: translateY(-50%); }
          .node-bottom { top: auto; bottom: 10px; transform: translateX(-50%); }
          .node-left { left: 18px; top: 50%; transform: translateY(-50%); }

          .loop-ring {
            position: absolute;
            left: 50%;
            top: 50%;
            border-radius: 50%;
            transform: translate(-50%, -50%);
            border: 1px dashed rgba(22,84,233,0.24);
          }

          .ring-outer { width: 408px; height: 408px; }
          .ring-inner { width: 324px; height: 324px; opacity: 0.5; }

          .loop-arc {
            position: absolute;
            width: 90px;
            height: 90px;
            border-top: 4px solid var(--primary);
            border-right: 4px solid var(--primary);
            border-radius: 0 90px 0 0;
          }

          .arc-1 { right: 98px; top: 72px; transform: rotate(26deg); }
          .arc-2 { right: 102px; bottom: 86px; transform: rotate(116deg); }
          .arc-3 { left: 110px; bottom: 92px; transform: rotate(206deg); }
          .arc-4 { left: 106px; top: 80px; transform: rotate(296deg); }

          .loop-footer {
            margin-top: 8px;
            padding: 16px 18px 14px;
            border-radius: 18px;
            background: rgba(15, 67, 194, 0.06);
          }

          .loop-quote {
            font-size: 24px;
            font-weight: 800;
            margin-bottom: 8px;
          }

          .loop-subquote {
            color: var(--muted);
            font-size: 16px;
            line-height: 1.55;
          }

          .summary-row {
            position: relative;
            z-index: 2;
            margin: 34px 108px 0;
            display: grid;
            grid-template-columns: repeat(3, 1fr);
            gap: 24px;
          }

          .summary-card {
            min-height: 276px;
            padding: 26px 22px 20px;
            border-radius: 26px;
            border: 1px solid var(--line);
            background: linear-gradient(180deg, rgba(255,255,255,0.98), rgba(247,250,255,0.96));
            box-shadow: var(--shadow);
            display: grid;
            justify-items: center;
            text-align: center;
            gap: 12px;
          }

          .summary-card-title {
            font-size: 36px;
            font-weight: 900;
            letter-spacing: -0.02em;
          }

          .summary-card-desc {
            font-size: 24px;
            font-weight: 700;
          }

          .summary-card-detail {
            color: var(--muted);
            font-size: 18px;
            line-height: 1.55;
            max-width: 360px;
          }

          .conclusion-rail {
            position: relative;
            z-index: 2;
            margin: 30px 106px 0;
            display: flex;
            align-items: center;
            gap: 18px;
          }

          .rail-line {
            flex: 1;
            height: 3px;
            border-radius: 999px;
            background: linear-gradient(90deg, rgba(22,84,233,0.08), rgba(22,84,233,0.72));
          }

          .rail-line.right {
            background: linear-gradient(90deg, rgba(22,84,233,0.72), rgba(22,84,233,0.08));
          }

          .rail-node {
            width: 16px;
            height: 16px;
            border-radius: 50%;
            background: var(--primary);
            box-shadow: 0 0 0 8px rgba(22,84,233,0.08);
          }

          .conclusion-banner {
            width: 920px;
            padding: 22px 28px;
            border-radius: 26px;
            background: linear-gradient(180deg, rgba(14, 67, 194, 0.95), rgba(21, 84, 233, 0.88));
            color: #fff;
            box-shadow: 0 24px 50px rgba(13, 55, 161, 0.24);
            text-align: center;
          }

          .conclusion-title {
            font-size: 30px;
            font-weight: 900;
            margin-bottom: 10px;
          }

          .conclusion-path {
            font-size: 18px;
            line-height: 1.55;
            opacity: 0.95;
          }

          .priority-panel {
            position: relative;
            z-index: 2;
            margin: 28px 108px 0;
            padding: 22px 24px 24px;
            border-radius: 26px;
            border: 1px solid var(--line);
            background: rgba(255,255,255,0.88);
            box-shadow: 0 18px 42px rgba(15, 43, 112, 0.08);
          }

          .priority-title {
            font-size: 26px;
            font-weight: 800;
            margin-bottom: 18px;
            text-align: center;
          }

          .priority-row {
            display: grid;
            grid-template-columns: repeat(4, 1fr);
            gap: 14px;
          }

          .action-pill {
            padding: 16px 14px;
            border-radius: 18px;
            background: linear-gradient(180deg, rgba(234,241,255,0.96), rgba(255,255,255,0.96));
            border: 1px solid rgba(22,84,233,0.14);
            font-size: 18px;
            font-weight: 700;
            text-align: center;
            line-height: 1.45;
          }

          .closing-note {
            position: relative;
            z-index: 2;
            margin: 28px 340px 0;
            display: flex;
            align-items: center;
            gap: 18px;
          }

          .closing-line {
            flex: 1;
            height: 2px;
            background: linear-gradient(90deg, rgba(22,84,233,0.04), rgba(22,84,233,0.45), rgba(22,84,233,0.04));
          }

          .closing-text {
            font-size: 26px;
            font-weight: 800;
            white-space: nowrap;
          }

          .bottom-band {
            position: absolute;
            left: 0;
            bottom: 0;
            width: 1160px;
            height: 54px;
            display: flex;
            z-index: 2;
          }

          .bottom-band-main {
            width: 1086px;
            background: linear-gradient(90deg, #0f43c2, #0b3cae);
          }

          .bottom-band-tail {
            width: 74px;
            background: linear-gradient(135deg, #0b3cae 50%, rgba(24, 90, 235, 0.12) 50%);
          }

          .bottom-band {
            height: 42px;
            width: 1120px;
          }

          .bottom-band-main {
            width: 1048px;
          }

          .bottom-band-tail {
            width: 72px;
          }

          #slide-05 .flow-board {
            margin-top: 28px;
            padding: 24px 26px;
          }

          #slide-05 .flow-step {
            min-height: 188px;
            gap: 10px;
          }

          #slide-05 .step-title {
            font-size: 25px;
          }

          #slide-05 .step-desc {
            font-size: 16px;
          }

          #slide-05 .case-row {
            margin-top: 18px;
          }

          #slide-05 .case-card {
            padding: 16px 18px;
          }

          #slide-05 .case-topline {
            margin-bottom: 14px;
          }

          #slide-05 .case-topline h3 {
            font-size: 23px;
          }

          #slide-05 .timeline-step {
            min-height: 94px;
          }

          #slide-05 .timeline-step span,
          #slide-05 .role-duty p,
          #slide-05 .case-note,
          #slide-05 .cycle-pill {
            font-size: 17px;
          }

          #slide-05 .case-note {
            padding: 12px 14px;
            font-size: 15px;
          }

          #slide-05 .timeline-step small,
          #slide-05 .cycle-pill,
          #slide-05 .case-duration {
            font-size: 14px;
          }

          #slide-05 .cycle-pill {
            min-height: 74px;
            padding: 10px 8px;
          }

          #slide-05 .feature-row {
            display: none;
          }

          #slide-06 .master-card {
            width: 722px;
            padding: 22px 24px;
          }

          #slide-06 .separation-stamp {
            top: 252px;
            width: 470px;
            padding: 20px 22px;
          }

          #slide-06 .stamp-title {
            font-size: 28px;
          }

          #slide-06 .stamp-desc {
            font-size: 17px;
          }

          #slide-06 .dispatch-links {
            padding-top: 14px;
          }

          #slide-06 .roles-grid {
            margin-top: 0;
          }

          #slide-06 .role-card {
            min-height: 250px;
            padding: 20px 20px 18px;
          }

          #slide-06 .role-title {
            font-size: 26px;
          }

          #slide-06 .role-subtitle,
          #slide-06 .role-duty p,
          #slide-06 .isolation-item,
          #slide-06 .feature-chip-desc {
            font-size: 16px;
          }

          #slide-06 .isolation-panel {
            display: none;
          }

          #slide-06 .isolation-column {
            padding: 18px 20px;
          }

          #slide-06 .benefit-grid {
            grid-template-columns: 1fr 1fr;
            gap: 10px;
          }

          #slide-06 .benefit-grid .feature-chip:last-child {
            grid-column: 1 / span 2;
          }

          #slide-06 .feature-chip {
            padding: 14px 16px;
            gap: 12px;
          }

          #slide-06 .feature-chip-title {
            font-size: 20px;
          }

          #slide-07 .pitfall-layout {
            margin-top: 18px;
            gap: 20px;
          }

          #slide-07 .pitfall-stack {
            grid-template-columns: 1fr 1fr;
            gap: 12px;
            align-content: start;
          }

          #slide-07 .pitfall-card {
            min-height: 126px;
            padding: 16px;
          }

          #slide-07 .pitfall-card:last-child {
            grid-column: 1 / span 2;
            min-height: 108px;
          }

          #slide-07 .pitfall-title {
            font-size: 21px;
          }

          #slide-07 .pitfall-rule {
            font-size: 15px;
          }

          #slide-07 .loop-panel {
            padding: 18px 20px 18px;
          }

          #slide-07 .loop-title {
            font-size: 26px;
          }

          #slide-07 .loop-graph {
            height: 336px;
            margin-top: 6px;
          }

          #slide-07 .loop-core {
            width: 214px;
            height: 214px;
            padding: 28px;
          }

          #slide-07 .loop-core strong {
            font-size: 30px;
          }

          #slide-07 .loop-core span,
          #slide-07 .loop-subquote,
          #slide-07 .loop-node {
            font-size: 16px;
          }

          #slide-07 .loop-node {
            width: 146px;
            gap: 8px;
          }

          #slide-07 .ring-outer {
            width: 356px;
            height: 356px;
          }

          #slide-07 .ring-inner {
            width: 286px;
            height: 286px;
          }

          #slide-07 .arc-1 {
            right: 92px;
            top: 66px;
          }

          #slide-07 .arc-2 {
            right: 96px;
            bottom: 80px;
          }

          #slide-07 .arc-3 {
            left: 104px;
            bottom: 86px;
          }

          #slide-07 .arc-4 {
            left: 100px;
            top: 74px;
          }

          #slide-07 .feature-row {
            display: none;
          }

          #slide-08 .header-block {
            padding-top: 110px;
          }

          #slide-08 h1 {
            font-size: 60px;
          }

          #slide-08 .subtitle {
            max-width: 1440px;
            margin-inline: auto;
            font-size: 25px;
          }

          #slide-08 .summary-row {
            margin-top: 26px;
            gap: 20px;
          }

          #slide-08 .summary-card {
            min-height: 226px;
            padding: 22px 18px 18px;
          }

          #slide-08 .summary-card-title {
            font-size: 34px;
          }

          #slide-08 .summary-card-desc {
            font-size: 22px;
          }

          #slide-08 .summary-card-detail {
            font-size: 17px;
          }

          #slide-08 .conclusion-rail {
            margin-top: 20px;
          }

          #slide-08 .conclusion-banner {
            width: 860px;
            padding: 18px 22px;
          }

          #slide-08 .conclusion-title {
            font-size: 28px;
          }

          #slide-08 .conclusion-path {
            font-size: 16px;
          }

          #slide-08 .priority-panel {
            position: absolute;
            left: 108px;
            right: 108px;
            bottom: 58px;
            margin: 0;
            padding: 18px 20px 20px;
          }

          #slide-08 .priority-title {
            font-size: 24px;
            margin-bottom: 14px;
          }

          #slide-08 .priority-row {
            gap: 12px;
          }

          #slide-08 .action-pill {
            padding: 14px 12px;
            font-size: 17px;
          }

          #slide-08 .closing-note {
            display: none;
          }
        </style>
      </head>
      <body>
        <main class="deck">
          ${slide05()}
          ${slide06()}
          ${slide07()}
          ${slide08()}
        </main>
      </body>
    </html>
  `;
}

async function ensureOutputDir() {
  await fs.mkdir(ASSETS_DIR, { recursive: true });
}

async function writeHtml() {
  await fs.writeFile(HTML_OUTPUT, buildHtml(), "utf8");
}

async function renderSlides() {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({
    viewport: { width: SLIDE_WIDTH, height: SLIDE_HEIGHT },
    deviceScaleFactor: 1,
  });

  await page.goto(pathToFileURL(HTML_OUTPUT).href, { waitUntil: "load" });
  await page.evaluate(async () => {
    if (document.fonts && document.fonts.ready) {
      await document.fonts.ready;
    }
  });

  const outputs = [
    ["slide-05", "slide-05-demo-flow.png"],
    ["slide-06", "slide-06-agent-collab.png"],
    ["slide-07", "slide-07-pitfalls.png"],
    ["slide-08", "slide-08-summary.png"],
  ];

  for (const [id, filename] of outputs) {
    const locator = page.locator(`#${id}`);
    await locator.scrollIntoViewIfNeeded();
    await locator.screenshot({
      path: path.join(ASSETS_DIR, filename),
      type: "png",
    });
  }

  await browser.close();
}

async function main() {
  await ensureOutputDir();
  await writeHtml();
  await renderSlides();
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
