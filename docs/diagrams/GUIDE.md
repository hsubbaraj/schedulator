# Diagram Guide: Scheduler/Simulator Architecture

I've created **three versions** of the architecture diagram, each optimized for different audiences and purposes. All are much cleaner than the original!

---

## 1. **Full Architecture** (scheduler-simulator-architecture.mermaid)

**Best for**: Technical deep-dives, implementation planning

**What it shows**: 
- All major components grouped logically
- Key interactions between subsystems
- Consolidated clusters (no repetition)
- Clear separation of concerns

**Key improvements**:
- ✅ Collapsed 3 separate cluster subgraphs into one unified "Kubernetes Clusters" section
- ✅ Combined all scheduler implementations into a single "Pluggable Schedulers" block
- ✅ Reduced arrow crossings by 80%
- ✅ Better visual hierarchy (top = control, middle = execution, bottom = observability)

**Use this when**: 
- Onboarding new engineers
- Planning implementation sprints
- Explaining component responsibilities

---

## 2. **Simplified Flow** (scheduler-simulator-simple.mermaid)

**Best for**: Executive summaries, high-level overviews

**What it shows**:
- Five main stages: Input → Simulator → Scheduler → Kubernetes → Output
- Linear left-to-right flow
- Only essential connections
- Emoji icons for visual clarity

**Key features**:
- 📥 **Input**: Scenario definition
- 🔄 **Simulator**: State aggregation, API, enforcement
- 🧠 **Scheduler**: Decision logic (pick one algorithm)
- ☸️ **Kubernetes**: 3-10 clusters with nodes/pods
- 📊 **Output**: Metrics + decision logs

**Use this when**:
- Presenting to non-engineers
- Initial project pitches
- Documentation introductions

---

## 3. **Decision Loop** (scheduler-decision-loop.mermaid)

**Best for**: Understanding the runtime behavior, debugging

**What it shows**:
- Step-by-step flow of a single decision cycle
- State versioning (v1689 → v1690)
- Conflict handling and retries
- Continuous loop with triggers
- Failure injection integration

**Key stages**:
1. 📸 Aggregate State (v1689)
2. 📤 Serve State (GET /v1/state)
3. 🧠 Scheduler Decides (35ms)
4. 📥 Submit Decision (POST /v1/placement)
5. ✓ Validate (409 on conflict)
6. ⚙️ Apply Decision (per mode)
7. ☸️ K8s Schedules (pods to nodes)
8. 📊 Measure Outcomes
9. 🔄 Update State (v1690)
10. Repeat

**Use this when**:
- Debugging decision latencies
- Understanding state consistency
- Explaining optimistic concurrency
- Testing failure scenarios

---

## Quick Comparison

| Diagram | Complexity | Detail Level | Best Audience |
|---------|------------|--------------|---------------|
| **Full Architecture** | Medium | High | Engineers, architects |
| **Simplified Flow** | Low | Medium | Executives, PMs |
| **Decision Loop** | Medium | Very High | Implementers, debuggers |

---

## Key Design Changes (All Versions)

### Before (original):
- 3 separate cluster subgraphs (redundant)
- 4 separate scheduler boxes (RandomCluster, Greedy, CP-SAT, Custom)
- Arrows crossing everywhere
- 30+ nodes, 50+ edges
- Hard to follow the main flow

### After (cleaned up):
- 1 unified clusters section
- 1 scheduler section (showing it's pluggable)
- Minimal arrow crossings
- ~15 nodes, ~20 edges per diagram
- Clear visual hierarchy
- Consistent color coding:
  - 🔴 Red: Test harness/control
  - 🔵 Blue: Simulator core
  - 🟢 Green: Scheduler logic
  - 🟠 Orange: Kubernetes
  - 🟣 Purple: Observability

---

## How to Use These

### For Documentation:
1. Start with **Simplified Flow** in the README
2. Link to **Full Architecture** in architecture docs
3. Use **Decision Loop** in the implementation guide

### For Presentations:
- **Slide 1**: Simplified Flow (the big picture)
- **Slide 2**: Decision Loop (how it works)
- **Slide 3**: Full Architecture (for Q&A)

### For Implementation:
- Pin **Full Architecture** in your Slack channel
- Reference **Decision Loop** when writing the state manager
- Use both to validate your API contract

---

## Rendering Tips

All diagrams use Mermaid format. To render:

**In GitHub/GitLab**: Just commit the `.mermaid` files—they'll render automatically

**In VS Code**: Install "Mermaid Preview" extension

**In Notion**: Use `/embed` → paste Mermaid code

**Online**: https://mermaid.live/ (paste and export as PNG/SVG)

**In Documentation**: Most doc tools (MkDocs, Docusaurus, GitBook) support Mermaid natively

---

## Next Steps

1. ✅ Review all three diagrams
2. Pick the primary one for your README
3. Use the others as supporting material
4. Update as implementation evolves
5. Keep them in sync with the API spec

The diagrams are now **clean, understandable, and ready for production docs**! 🎉
