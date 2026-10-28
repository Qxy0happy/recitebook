---
name: 循环背诵
description: 一本装订起来的试卷簿——骨白纸页、朱红印章、页边密封线。
colors:
  ink: "#191a17"
  ink-soft: "#4f524a"
  muted: "#5d6057"
  paper: "#f7f5ee"
  paper-shade: "#f1eee3"
  desk: "#e3e0d8"
  rule: "#d5d1c2"
  rule-strong: "#c2bdac"
  seal-red: "#b02c20"
  seal-red-deep: "#8f2318"
  seal-wash: "#f2e3de"
  correction-green: "#2c5a48"
  correction-wash: "#e6ece5"
typography:
  display:
    fontFamily: 'Helvetica, "Helvetica Neue", Arial, "LXGW WenKai", serif'
    fontSize: "44px"
    fontWeight: 400
    lineHeight: 1.2
    letterSpacing: "0.03em"
  stem:
    fontFamily: '"Times New Roman", Times, "LXGW WenKai", serif'
    fontSize: "22px"
    fontWeight: 400
    lineHeight: 1.8
    letterSpacing: "0.01em"
  body:
    fontFamily: 'Helvetica, "Helvetica Neue", Arial, "LXGW WenKai", "PingFang SC", sans-serif'
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.65
    letterSpacing: "0em"
  label:
    fontFamily: 'Helvetica, Arial, "LXGW WenKai", "PingFang SC", sans-serif'
    fontSize: "10px"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "0.16em"
  numeral:
    fontFamily: 'ui-monospace, "Cascadia Mono", Consolas, "LXGW WenKai", monospace'
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "0em"
rounded:
  paper: "0px"
  control: "0px"
spacing:
  xs: "6px"
  sm: "12px"
  md: "22px"
  lg: "32px"
  xl: "48px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "#fbf8f1"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "0 22px"
    height: "50px"
  button-primary-hover:
    backgroundColor: "#000"
  button-cover:
    backgroundColor: "{colors.seal-red}"
    textColor: "#fbf8f1"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "0 22px"
    height: "50px"
  button-cover-hover:
    backgroundColor: "{colors.seal-red-deep}"
  button-quiet:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink-soft}"
    rounded: "{rounded.control}"
    padding: "0 22px"
    height: "44px"
  field:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    typography: "{typography.stem}"
    padding: "6px 0 8px"
  stamp:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink-soft}"
    typography: "{typography.label}"
    padding: "1px 7px"
---

# 循环背诵 — Design System

## Overview

**Creative North Star: "密封卷" (the sealed exam booklet).**

The app is a bound exam booklet lying on a desk. Every screen is one sheet: bone paper on a grey-brown desk, ink rules, a red seal, a green correction. The visitor is a Chinese postgraduate-entrance exam candidate working through the 29 Lists of a paper vocabulary book, usually at a desk in daylight and on a phone in transit.

The identity lives in three things only: the page margin (a dashed seal line carrying 29 punched holes and List numbers), the typography (Helvetica for the working label layer, Times New Roman for question bodies, LXGW WenKai for Chinese headings and prose), and the stamp (one rotated red mark that records the round and moves to the current List).

What it refuses: the dashboard of the category — a hero metric, a 29-cell status grid, and a status table all saying the same thing. The queue is one cover plus one table of contents; the margin already carries the whole index.

The world is a document, not a costume. No paper texture, no torn edges, no photographs, no illustration. Depth comes from one soft drop shadow under the sheet and a 1px top highlight; motion is one press of the seal and one width transition on the progress rule.

## Colors

Restrained, document-first. Colour does real jobs and never decorates.

- **Ink `#191a17`** — all text and the primary rules, never pure black; the only #000 in the system is the ink button's hover state.
- **Seal red `#b02c20`** — the current List, 待清 counts, wrong answers, and the cover's 开始背诵. It is the only saturated colour on most screens, so it always means "attention here".
- **Correction green `#2c5a48`** — 已清 and correct answers, with the wash `#e6ece5` behind a correct option.
- **Paper `#f7f5ee` / shade `#f1eee3`** — the sheet and its quiet fill (hover, chosen option, zebra-free rows).
- **Desk `#e3e0d8`** — the surface behind the sheet; never inside it.
- **Rules `#d5d1c2` (solid) and `#c2bdac` (dotted)** — hairlines do the dividing; cards do not exist.
- **Muted `#5d6057`** — secondary text; it holds ≥5.5:1 on paper.

Red and green are never adjacent as a pair except in a verdict line, where the mark carries the meaning.

## Typography

Three faces, three jobs, no exceptions.

- **Helvetica** is the working layer: brand, headings, labels, counts, buttons, English chrome. It is the neutral voice of a printed form.
- **Times New Roman** is the exam paper: question stems, options, answer lines, word cards. Its only use is content the candidate reads as a test.
- **LXGW WenKai** (self-hosted) is the Chinese voice for headings and prose; Chinese inside Helvetica/Times stacks falls back to it, so a mixed line keeps one hand.
- **Monospace with tabular figures** carries every count (待清, 首错, 第 3 / 11 题, page numbers). Counts never drift between rows; the stack ends in LXGW WenKai, so Chinese inside a count line (`队列第 1 位 · 待清 3 题`) keeps the same hand as the prose around it.

Scale: cover 44, screen heading 34, section 24, stem 22, body 14, label 10. Letter-spacing is reserved for labels (0.16em) and the round stamp (0.2em); body text is untracked.

## Layout

One sheet, two columns: a 78px seal margin and the page. The margin is sticky for the full viewport and holds the complete 29-List index; the page scrolls.

- Page padding 26 / 56 / 0 / 46, narrowing fluidly with `clamp()`.
- The cover is a 3-column grid: number, title block, action block. It collapses to two columns and a full-width action below 860px.
- The contents table is a 5-column grid: number, title, 待清, 首错, status. On phones the 首错 column is dropped, not wrapped.
- Below 640px the seal margin becomes a horizontal, scrollable index strip under the page head.
- Vertical rhythm: 26px between sections, 16px inside a section head. Headings always get more space above than below.

## Elevation & Depth

Two levels only.

- **The sheet** sits on the desk with `inset 0 1px 0 #fffdf7, 0 1px 2px rgba(42,40,32,.10), 0 24px 60px rgba(42,40,32,.16)`. It is the only element with a shadow.
- **Nothing inside the sheet casts a shadow.** Grouping is done with 1px rules, dotted separators, and label spacing. A boxed card inside the page would read as a second sheet.

Hover and selection use a paper shade fill, never a shadow or a lift.

## Shapes

Square. Radius is 0 on every control and every panel; only the seal margin's punched holes and the current-List ring are circles. The ring is a 1.5px ellipse rotated −8°, drawn like a hand stamp, and appears exactly once on screen.

Borders are 1px; the only 2px rules are the page head underline and the feedback top rule, which mark a change of document section.

## Components

- **Seal index (`.seal`)** — 29 items, each a 6px punched hole plus a 9px number. Filled hole plus green number = 已清; red hole = 待清/进行中; the current item carries the ring. Desktop: vertical, sticky; phone: horizontal strip.
- **Cover (`.cover`)** — the next List: 62px number, 44px WenKai title, one meta line (queue position, 待清, 首错), and the only red fill in the app. The round stamp sits above the action, rotated −5°.
- **Contents row (`.toc-row`)** — number, WenKai title, right-aligned tabular counts, stamped status. The next row is underlined with ink; cleared rows drop to muted.
- **Stamp (`.stamp`, `.stamp-round`)** — 1px currentColor box, 9px letterspaced text; the round stamp is the same idea rotated and thicker.
- **Options (`.option`)** — Times body at 19px, Helvetica small-caps-like letter key in a 46px gutter, dotted separator, ✓/✕ in the right gutter. Correct = green wash; wrong = red struck text.
- **Fields** — bottom rule only, transparent fill, label 10px above; focus turns the rule red and the caret red.
- **Buttons** — filled ink (primary), 1px ink-soft outline (quiet), or an underlined text action. Only the cover action is red. Height 44/50; no radius, no shadow.

## Do's and Don'ts

**Do**

- Let the seal margin carry the index; never add a second list of the same 29 Lists.
- Use one red mark per screen as the attention signal.
- Keep counts in tabular monospace and right-aligned so columns read as columns.
- Set question content in Times New Roman and UI chrome in Helvetica.
- Keep the sheet flat; separate with rules, not boxes.

**Don't**

- Don't reintroduce cards, pills with radii, gradients, or a hero metric.
- Don't put red and green next to each other outside a verdict.
- Don't add a kicker above a heading; the heading carries its own weight.
- Don't scale type for hierarchy when weight, case, and reversal can do it.
- Don't animate more than the seal press and the progress rule.
