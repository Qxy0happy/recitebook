---
version: 1
slug: "wails-frontend-src-app-tsx"
primary_target: "wails/frontend/src/App.tsx"
related_targets: []
---

# 循环背诵 · 队列与作答

Mode: Operate. Whole-app visual world replacement (queue, 略览词卡, 答题/反馈, 本轮结果, 设置, 备份, 同步, 询问).

## Direction contract

THESIS: 应用是一本装订起来的试卷簿。左页边一条密封线，29 个 List 是线上的装订孔与编号，队首那孔盖朱红圆章；队列是封面加目录，做题是翻开的正文页，判分是页边落下的一笔红。拒绝「仪表盘 + 29 格色块 + 状态表」三套东西说同一件事。

OWN-WORLD: 骨白纸 #f7f5ee 压在灰褐桌面 #e3e0d8 上，纸边靠 0 24px 60px 的落影与 1px 高光浮起；墨 #191a17；朱红 #b02c20 只给「待清 / 答错 / 当前」以及封面的「开始背诵」，深绿 #2c5a48 只给「已清 / 答对」；线只有两种：1px 实线 #d5d1c2 与 1px 点线 #c9c5b6。文楷承担中文，Helvetica 承担英文标签与封面数字，Times New Roman 承担题干与选项，等宽数字承担所有计数。组件词汇只有三种：纸页、页边索引、印章。

STORY: 打开就知道今天进哪个 List、哪一页、还剩几道；做完一题立刻在页边看到红笔与正确答案；清空一个 List 时页边圆章移到下一个编号，队列自然收窄。

FIRST VIEWPORT: 整幅骨白纸页。左侧 78px 密封线：29 个装订孔与编号竖排，当前编号套朱红圆章，底部刻「密封线」。右侧页眉：品牌「循环背诵」+「考点词 · 29 个 List」+ 一枚「第 N 轮」章 +「备份 · 设置」。页眉下是队首封面：大号 List 编号、44px 文楷标题、一行事实（队列第 N 位 · 待清 X 题 · 首错 Y 题；词数与题数只在进入 List 后可见，队列页不伪造）、朱红实心「开始背诵」、右上斜盖「第 N 轮」印章。再下是目录：按队列顺序列出 List 编号、标题、待清、首错、状态章，点线分隔，队首行用 1px 墨线上提；手机宽度下只保留待清与状态两列，首错留在封面。页脚：本轮账与「开始新一轮」。手机宽度下密封线变成横向可滚动索引条，页眉保留「第 N 轮」章。

FORM: 共振排序第 4 个方向（第 1 答题卡、第 2 密封卷同行不同纸、第 3 稿纸、第 4 本方向、第 5 借阅卡、第 6 剪贴本、第 7 活字盘），seed: 7197454a3880b559。

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance.
