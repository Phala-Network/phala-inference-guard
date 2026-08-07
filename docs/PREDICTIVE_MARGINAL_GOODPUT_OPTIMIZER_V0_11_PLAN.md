# PIG v0.11.4 确定性请求感知与 Prefill 干扰准入计划

状态：**唯一 canonical 执行规范；v0.11.3 Router canary 已失败并回退，禁止重新晋级；v0.11.4 corrective 已完成 behavioral red/green、三遍 final review、exact-source builder matrix、source commit/push/annotated tag、clean-tag builder-local/registry immutable image provenance、部署前 live baseline/候选、Router-disabled 专用验证 harness 和 30 分钟 canary observer 三遍静态复查；GitHub Publish Image #26 的 terminal manifest HEAD denied 继续客观记为红状态，但 raw job log、BuildKit build record、registry image/config/binary 已形成 exact cross-provenance，用户已于 2026-08-07 指示继续，因此该异常只作为 v0.11.4 一次性非阻断 release-process 记录；禁止重跑、重推或移动 v0.11.4 tag，harness/observer 尚未运行，未部署或重新启用 use1-cb**
最后更新：2026-08-07
仓库：`phala-inference-guard`
默认 vLLM poll interval：`500 ms`

本文档取代此前所有 v0.11 Safe Envelope、pressure bucket、RLS、marginal-goodput
learning 和旧 dynamic QoS 方案。文件名为历史遗留，正文才是当前合同。任何算法、
门禁、默认值或证据变化必须先更新本文档。第 1--12 节与第 13 节开头的 current
checklist/summary 是 active contract；第 13.1 节以后只是按时间追加的证据账本，不反向覆盖
前文。压缩上下文后先读 active contract 和最新账本尾部，不需要重读全部历史 red/green。

## 1. 目标

PIG 在请求进入 vLLM 之前，使用：

- 最新、有效的 vLLM 容量与负载快照；
- 尚未被上游指标吸收的本地 reservation；
- 快速、模型无关的输入大小近似值；

作出确定性的逐请求 `ADMIT` 或 `PROTECT`：

1. 不突破明确的 KV 保护边界，不在 preemption cooldown 中继续扩张；
2. TPS 可以从 target 小幅下降；有效 TPS 越接近 floor，新 intake 越偏向小请求；预测值到达
   floor 时暂不新增 intake，但这仍是随下一 fresh snapshot 自动恢复的逐请求选择性保护，
   不是由一个后反馈样本永久关闭 intake；
3. 容量有压力时优先拒绝大输入请求，而不是不区分请求地全部拒绝；
4. KV、waiting、TPS 均无压力时，普通请求保持 work-conserving；但同一 500-ms snapshot 内的
   `<64K` regular burst 也不得无限累积，未与 long Prefill 重叠时与 weighted 请求共用 256K
   aggregate pending-prefill 干扰预算；64K/256K/512K 分层继续生效，backend 真正 idle 时允许
   首个 512K/650K 请求进入，避免低流自锁和 GPU 闲置；
5. 在上述 QoS 约束下，提高 SLO-compliant completion throughput 和总吞吐。

PIG 不路由、不排队、不重排请求，也不修改 Router/vLLM 源码。因此这是同步、在线、
work-conserving 的贪心准入，不宣称对未来未知请求实现全局最优。

## 2. 明确不做什么

v0.11.4 不包含：

- 任何在线学习、回归、校准学习、risk price、置信区间或 exploration/probe；
- learned KV/decode/concurrency limit、Safe Envelope、pressure bucket、frontier；
- cache hit/cache-aware admission；
- TTFT admission；
- premium/basic tier、lane、priority 或 backend priority 注入；
- 模型专用 tokenizer asset、模型 profile learner；
- PIG 内部 queue、SJF/SRPT、backend selection；
- 依请求、模型或 prompt cardinality 无界增长的状态。

这里的长输入成本不依赖模型专用 tokenizer asset，也不要求 Router 提供 prefix-match/cache
信息。没有可信 cache 信息时，PIG 把快速估算的总 prompt tokens 作为保守的
`estimatedPrefillTokens`；它不是精确的 `uncached_tokens`，日志、metrics 和文档不得混淆两者。
64K/256K/512K/650K 合同用于通用多卡大模型；当前 `use1-cb` 的 262K max-model-len 不能作为
删除 512K/650K 分层的理由。
650K 是 `>=512K` quiescent 档的代表用例，不是第五个阈值。v0.11.3 在真实 canary 中出现
约 80 秒 Decode freeze；零 preemption 不等于 QoS green，也不能覆盖生成 TPS 降至零的失败。

旧学习实现最终必须从 v0.11 production factory、HTTP admission、配置和 metrics 的可达路径中
删除，不能只靠 runtime flag 假装 disabled。若静态 reachability 证明 legacy 源码只被旧模式或
历史 tests/simulation 消费，本版不为代码洁癖大删数千行；过去的 focused green 也不是本计划
的实现证据。

## 3. 本次复查删掉的过度设计

上一版计划方向正确，但以下内容不适合作为第一版：

1. **把近似值叫 hard upper**：当前 `EstimatedInputHigh` 来自 JSON 字节量、tool、
   template 和 modality surcharge；它是保守估计，不是对所有 tokenizer/template 成立的
   数学上界。正文改称 `estimatedPromptCost`，硬 KV 保护必须另留 margin；
2. **把平均值叫单用户 TPS**：`generationTokensDelta / running` 只是
   `meanActiveTPSProxy`，不能证明每个用户的 TPS。它只作为当前 QoS 压力信号，真实
   per-request TPS 仍在离线/实际流量验证；
3. **过度限定 TPS 样本**：waiting 或 reservation 存在时直接废弃 TPS，会在高负载时
   失明。现在只要求 counter/epoch/time 有效，并对 running 变化使用保守 denominator；
4. **重复的 decode 保护**：删除独立 `decodeGuard + decodeLookahead + context contract`
   三套机制，直接复用现有 bounded output reservation；
5. **虚构 sequence hard capacity**：当前启动指标能确认 KV token capacity/block size，
   不能确认 vLLM `max_num_seqs`。第一版不增加 guessed sequence hard limit；
6. **参数过多**：删除 minimum intake、minimum selective KV、minimum selective input 和
   waiting emergency；当前方案仅保留有 64K/256K/512K 行为合同与 simulation 证据的四个
   bounded Prefill threshold/budget，不恢复旧 generic prefill maximum；
7. **组件过多**：不预先建立八个接口；只保留 estimator、observer、pure policy、
   manager/reporting 四个实际职责；
8. **过度实验流程**：删除在线 learner 式 grid search 和“先估 variance 再定门槛”的复杂
   流程。先用少量预注册配置做确定性对照，再由 shadow/canary 验证真实效果；
9. **过度 reservation 重写**：已有 Manager 已覆盖原子 reserve、forward、prefill、
   reconcile、terminal。先验证并去掉 learning coupling，不另造第二套生命周期。
10. **单样本 TPS 全局锁**：`meanActiveTPSProxy` 不是逐用户 TPS，单个 500-ms 样本低于
    floor 不足以证明必须停止所有 intake。floor 改为 `pressure=1` 的选择性保护，只有
    stale、preemption 和 hard KV 等可验证安全条件才能全局 `HARD_PROTECT`；
11. **把所有 epoch 变化都永久锁死**：model identity、KV capacity 或 block size 漂移会使
    当前 observer/policy 合同失效，仍必须 fail closed 并重建 PIG lifecycle；但同一 identity、
    capacity、block size 下 generation/preemption counter 回退通常只是 vLLM restart。后者必须
    原子清除旧 reservation/retired state，以 reset sample 重建 Manager base 和 counter baseline，
    成功后自动 reopen。rebase 前 fail closed；rebase 后 TPS 先保持 unknown-neutral，不凭任意
    sequence cap 限流；每个候选仍受 post-admit hard KV、waiting/KV pressure、本地 reservation
    和下一 500-ms snapshot 约束。不能永久误锁，也不能静默复用旧 epoch 容量；
12. **过宽的旧配置删除范围**：只删除或停止读取 predictive learning/TTFT/calibrator 的
    专用配置与 metrics；仍被其他 dynamic/KV 模式消费的公共配置不得顺手删除；
13. **500-ms 配置写了但没接线**：新 factory 必须读取
    `PredictiveObservationPollInterval` 和 `PredictiveMaximumMetricsAge`，不得继续读取旧
    `DynamicPollInterval`/KV freshness 字段。
14. **size allowance 可超出剩余 KV 的文档漏洞**：SELECTIVE allowance 必须由
    `remainingKV` 封顶；hard-fit 仍先比较完整 request reservation；
15. **把最后一次拒绝做成 Router 粘滞状态**：若大请求被拒后 Router 不再送请求，PIG 将
    没有新请求来解除保护。Router capacity 不永久复用 last verdict；每次 metrics scrape
    以当前 observer snapshot 和 Manager virtual state 做一个无 reservation 的 one-block
    inspect，再发布 `OPEN`、`SELECTIVE` 或 `HARD`。这是恢复机制，不是 exploration/learning。
16. **用全部 remaining KV 缩放 size allowance 的量纲错误**：R58/R59 simulation 证明，
    例如剩余 KV 为 7 万 token 时，20% TPS pressure 仍给出约 5.6 万 token allowance，普通
    small/large 请求几乎都感受不到 QoS 保护。SELECTIVE 的输入阈值改由
    `hardKVLimit-softKVLimit` 这一明确的弹性容量带缩放，再由 `remainingKV` 封顶；
17. **pressure=1 仍固定放行一个 block 会持续穿透 floor**：同样大小的极小请求可在每个
    500-ms epoch 反复进入，并把一次可容忍下降变成持续 QoS 退化。完整压力时 allowance
    必须为零；这仍是可随下一 fresh snapshot 立即恢复的 `SIZE_PROTECT`，不是 stale、
    preemption 或 hard-KV `HARD_PROTECT`，也不形成 Router 粘滞锁；
18. **TPS 只看当前均值仍是后反馈**：observer 必须保留 generation delta 得到的
    `aggregateTPSProxy`，Manager 必须把已观测 running 与尚未吸收的 reservation 合成
    `effectiveSequences`；observed waiting 继续作为独立 pressure，不能再次放入 TPS
    denominator 重复收费。当当前 TPS 已低于 target 或存在 waiting 时，用
    `aggregateTPSProxy/(effectiveSequences+1)` 预测新请求进入后的均值；健康、无 waiting
    且当前 TPS 不低于 target 时采用 work-conserving 假设，预测值保持当前均值，让系统先
    增加一个请求再由下一 500-ms snapshot 校正。该规则无学习、无历史窗口、无模型资产。
19. **用 post-admit KV 同时做 soft pressure 和 hard-fit 会对大请求双重收费**：R63 中
    `large-only` 和 `large-small-output` 在 preemption/TPS-floor 没有改善时吞吐分别退化约
    21% 和 20%。完整 request reservation 继续用于不可覆盖的 post-admit hard-KV 判断；
    soft `kvPressure` 只使用已观测加本地 reservation 的当前 `effectiveKV`。健康区可以
    work-conserving 地跨入一次弹性带，下一请求会在同一 Manager 锁内看到前序 reservation。
20. **健康 TPS 分支把同一 poll 内整批 burst 都当成第一个请求**：只要当前
    `meanActiveTPSProxy >= target` 且 `waiting=0`，旧公式就完全忽略不断增加的
    `effectiveSequences`，并不能兑现“先增加一个请求再校正”。新规则只在没有尚未被
    snapshot 吸收的本地 sequence 时采用一次 work-conserving 预测；同一 snapshot 的后续
    请求改用 `aggregateTPSProxy/(effectiveSequences+1)`，避免 500-ms poll 前无限乐观扩张；
21. **`BlockSize` 是未消费的死配置**：既然 request reservation 与 vLLM KV capacity 都按
    block 工作，soft/hard operational limits 必须向下对齐真实 `blockSize`。否则配置违反
    SOLID，边界 telemetry 也会显示不可实际分配的零碎 token；
22. **simulation 容差量纲混用**：一个 100-ms tick 只用于 duration budget；goodput 比较只用
    相对阈值和极小浮点 epsilon，不能把 `0.100001 seconds` 加到 tokens/s；suite aggregate
    budget 不能把逐场景容差累加；
23. **release gate 顺序与 test-first 执行不一致**：focused simulation 应在最终 full matrix
    之前暴露算法问题；只有算法、生命周期和 simulation 稳定后，才在同一个 exact archive
    上执行 full/vet/race/build/benchmark/simulation 最终矩阵。
24. **adapter 的二次 closed check 仍留有 reserve-to-forward TOCTOU**：`Decide()` 在 reserve
    后检查一次 `closed`，但 `Close()` 仍可在线程返回 reservation 后、真实
    `MarkForwarded()` 前发生；当前 reservation 只持有 Manager，Manager 的
    `MarkForwarded()` 又允许 intake 已关闭的既有 reservation commit。request-aware
    reservation 必须通过 owner 锁把 close 与 forward commit 线性化：close 先发生则
    `MarkForwarded()` 失败并由 HTTP deferred terminal 释放；forward commit 先发生则允许该
    已提交请求按正常 lifecycle 收尾。该修复不增加第二套 reservation registry。
25. **真实 enforce HTTP 在预测后仍进入旧 QoS/tier/priority 热路径**：当前 proxy 先创建
    request-aware reservation，再调用旧 `qosGate.WaitAcquire`，因此已承诺的请求仍可能排队或
    被 static global/tier gate 一刀切拒绝，并继续执行 backend priority rewrite。这与“PIG 不
    排队、v0.11 不含 tier/priority、request-aware policy 是 enforce 准入控制器”的合同冲突，
    也会让 reservation 在本地旧队列中虚占容量。v0.11 `enforce` 必须跳过旧 gate、tier
    accounting、tier/lane header 和 backend priority injection；`shadow` 保留原服务行为，确保
    观察模式无副作用。旧 dynamic/learning 源码若无 production factory 可达调用，本版先保留
    为历史 tests/simulation，避免用大规模删除掩盖热路径问题；release 审计以 non-test
    production reachability 为准。
26. **Router 仍被旧 dynamic limit/waiting 二次控制**：即使 enforce HTTP 已绕过旧 QoS gate，
    Router 仍消费 `pig_dynamic_observed_running`、`pig_dynamic_observed_waiting` 和
    `pig_dynamic_global_limit`。若继续原样发布旧 dynamic snapshot，则 OPEN 仍会被旧
    global limit 限流；更严重的是 SELECTIVE 常由 `waiting>0` 触发，而 Router 会因为同一个
    waiting 把节点整体判为 blocked，使后续小请求无法到 PIG 做逐请求判断。v0.11 enforce 必须
    把 Router-consumed 字段视为 request-aware policy 的派生 projection：旧 waiting/global
    limit 只保留 raw observability；effective waiting 恒为零；OPEN 的 effective global limit
    使用 Router 已支持的非正值表达“无外层容量钳制”；SELECTIVE 发布
    `effectiveRunning+1`，HARD 发布 `effectiveRunning`。`shadow`/`off` 继续原样发布旧 dynamic
    snapshot。这样 Router 只做 PIG verdict 的粗粒度流量适配，不成为第二个 admission
    controller，也不会因 raw waiting 破坏 request-size differentiation。
27. **enforce 仍被已禁用的 priority-only validator 耦合**：production derived config 已在
    request-aware enforce 中关闭 backend priority injection，但顶层 `Validate` 仍会因无效的
    priority mode/strategy/field/buffer/limit 拒绝启动。v0.11 enforce 不应被不可达功能的语义
    配置阻断；`validatePriorityConfig` 必须在 enforce 时跳过 priority-only 校验，shadow/off 继续
    严格验证。OpenAI compatibility 是独立能力，其字段、校验和 body rewrite 不随 priority
    injection 一起删除或跳过。
28. **Router inspect 与随后 Manager snapshot/publication 不是跨请求原子事务**：one-block
    inspect 在 Manager 锁内对 sequence `S` 求值，锁释放后另一个请求可能 reserve/terminate，
    telemetry 再读取较新的 virtual state。该窗口不能让请求逃逸到 upstream，因为真实 HTTP
    每次仍在 Manager 锁内重新执行 policy+reservation；最坏是一个 metrics scrape 周期内多送
    一个到 PIG 后被 429，或短暂少送一个请求。下一次 scrape 会用 fresh snapshot 重算，不能
    形成 sticky clamp。为消除 advisory staleness 而跨 observer/metrics 长持 Manager 锁，会扩大
    admission 延迟且仍无法使 Router scrape 与下一次网络请求原子化，本版不引入该复杂度；以
    concurrent scrape/admit/terminate race test、最终无 reservation leak、fresh OPEN 恢复和真实
    HTTP atomic-admission tests 固化接受边界。
29. **PIG coarse `/v1/upstream-status` 仍由旧 QoS controller 决定**：Router 主选择读取的 metrics
    已切到 request-aware projection，但其他下游可消费的 PIG coarse status 仍检查 static limit、
    old dynamic waiting/state、legacy queue 和 tier。这样同一个 enforce OPEN 可能在 metrics 中
    neutral，却在 status endpoint 被旧算法报 red。enforce status 必须复用当前 request-aware
    inspect：OPEN=`green`、SELECTIVE（one inspect slot）=`yellow`、HARD/availability=`red`；
    predictive provider 缺失时 fail closed 为 `red`。off/shadow 保持既有 status 行为，不能影响
    旧模式兼容性。
30. **把 TPS unknown 强制收缩为单次 speculation 是过度保护**：R93 的 `pre-poll-burst`
    中，该规则只把已在允许预算内的 TPS-floor violation 从 `0.1 s` 降到 `0`，却让 completion
    tokens/s 从 `102.405` 降至 `70.630`，约退化 31%。v0.11 不猜 `max_num_seqs`，也不增加任意
    cold-start cap。TPS unknown 继续 neutral，由 startup baseline 尽快缩短 unknown 窗口，并由
    post-admit hard KV、当前 waiting/KV pressure、本地 reservation 和下一 500-ms snapshot 保护。
    simulation 必须把 burst 纳入 goodput non-regression gate，不能用不必要的 QoS 改善换取大幅
    吞吐下降。
31. **startup probe 没有成为 TPS counter baseline**：startup 已提供同 identity 的 generation、
    preemption、running 和 observed time，但 observer 未设置 request-aware baseline flag，导致
    第一轮 500-ms poll 仍无法计算 TPS。构造成功时必须把 startup sample 标为 baseline；无有效
    startup observation 时继续按 cold-start 规则处理。
32. **dead Router hold 配置仍能阻断 v0.11 启动**：request-aware Router projection 每次 scrape
    fresh 重算，没有固定 hold；旧 `PREDICTIVE_ROUTER_BACKPRESSURE_HOLD` 只残留在旧 adapter/
    日志节流代码，却仍由 v0.11 loader/validator 解析并可导致启动失败。v0.11 production config、
    startup log 和 failure logging 不得再依赖该字段；日志去重使用内部有界常量。保留仅供不可达
    legacy unit tests 使用的 helper 不算 production config。
33. **canonical 文档把合同和 900 多行历史账本混成同一阅读面**：证据不能删除，但 13.1 以后
    明确为 non-normative append-only ledger。后续恢复任务只读 active contract、current checklist
    和最新账本尾部，避免旧方案/旧 false-green 在压缩上下文后重新成为实现要求。
34. **完成窗口的 TPS proxy 会在低流时误触发 invalid hard protect**：observer 按合同允许
    `previousRunning>0、currentRunning=0` 且 generation 增长的窗口产生 proxy，但 pure policy
    把 `TPSValid && Running==0` 直接判 invalid。结果是后端已空闲的 fresh snapshot 反而拒绝下一
    请求，直到再下一 poll 才恢复。当前没有 effective sequence 时，完成窗口的 TPS 只保留为
    observation，admission 按 TPS unknown-neutral；第一个 hard-fit 请求可 work-conserving 进入。
    同 snapshot 出现本地 reservation 后，后续请求可以再用该 aggregate proxy 与新的
    `effectiveSequences+1` 做 post-admit forecast。KV、waiting、stale、preemption 保护不变。
35. **pure policy 没有独立守住 KV margin 不变量**：production KV config 已通过
    `target>0`、`hard<emergency<=1` 间接保证 `0 < soft < hard < 1`，但
    `RequestAwareConfig.Validate()` 仍允许 `soft=0` 或 `hard=1`。factory 当前不可达这些值不代表
    domain policy 可以依赖外层偶然校验；pure policy 自身必须拒绝二者，避免未来复用时悄悄
    移除 soft band 或 hard margin。不新增第三个 ratio 或其他配置。
36. **regular multimodal burst 被 lexical hint 与长 Prefill gate 双重漏计**：estimator 已识别
    `image_url/input_image/audio_url/input_audio/video_url/input_video` 并把 bounded modality
    allowance 放进 `EstimatedInputHigh`，但 request-aware adapter 仍只消费 URL/text lexical hint；
    同时 `<64K regular` 完全绕过 256K aggregate pending-prefill budget。v0.11.3 canary 因而能在
    同一 observation 周期连续 reserve 大量看似很小的 multimodal regular 请求。v0.11.4 对纯文本
    继续使用 bounded lexical hint；只要已识别 modality，就使用既有 `EstimatedInputHigh` 作为
    Prefill interference estimate，hard-KV reservation 语义不变。regular-only aggregate 在
    post-admit 超过 256K 时 `SIZE_PROTECT/prefill_budget`；已有 exclusive Prefill 时仍允许短请求，
    避免用一个长请求把 short goodput 全部锁死。
37. **current gauge 实际发布 last-decision snapshot**：v0.11.3 的
    `pig_predictive_request_aware_pending_prefill_sequences/tokens` 来自 `lastRequestAware`；请求结束且
    没有新请求时不会清零。v0.11.4 令这些既有 pending gauges（包括无前缀的
    `post_admit_pending_prefill_tokens`）从唯一 Manager reservation/base 状态实时派生，并新增显式
    `last_decision_pending_*` gauges；status 同样拆成
    `prefill_current` 与 `prefill_last`。Router backpressure 仍只消费独立 current inspect projection，
    不从 last-decision 或 pending gauge 猜测容量。

## 4. 输入事实

### 4.1 vLLM snapshot

复用当前 parser/observer 的事实：

```text
capacityTokens  <- vllm:cache_config_info{kv_cache_size_tokens=...}
blockSize       <- vllm:cache_config_info{block_size=...}
usedTokens      <- round(kv_cache_usage_perc * capacityTokens)
running         <- vllm:num_requests_running
waiting         <- vllm:num_requests_waiting
preemptions     <- vllm:num_preemptions_total
generation      <- vllm:generation_tokens_total
modelIdentity   <- required unique model_name hash
observedAt
```

`capacityTokens` 和 `blockSize` 是显式指标；`usedTokens` 仍受 percentage gauge 舍入和
500-ms 采样间增长影响。因此：

```text
softKVLimit = blockRoundDown(capacityTokens * softKVRatio, blockSize)
hardKVLimit = blockRoundDown(capacityTokens * hardKVRatio, blockSize)
```

其中 `hardKVRatio < 1`，留出的 margin 覆盖指标误差、block 粒度和下一次 poll 前的增长。
向下对齐 block 后，hard-fit、remaining KV 和日志/metrics 使用同一个可执行边界。
这是一条保守 operational guard，不宣称可以形式化证明 vLLM 永不 preempt；真正结果仍
由 preemption counter 和模拟/实际流量验证。

snapshot stale、capacity/block/model identity mismatch 或 epoch transition 时先 fail closed。普通
scrape 失败在新的完整 fresh snapshot 后恢复；model identity/capacity/block drift 要求重建
observer/PIG lifecycle。同 identity/capacity/block 的 generation/preemption counter reset 使用
reset sample 原子 rebase Manager 和 observer baseline，清除旧 epoch reservation/retired state 后
自动 reopen；rebase 失败继续 fail closed。

### 4.2 请求成本

直接复用 bounded JSON estimator：

```text
selectionInputTokens = EstimatedInputHigh，已识别 modality 时
                     = ApproximateInputTokens，纯文本 hint 已知且 > 0 时
                     = EstimatedInputHigh，hint 不可用时的保守 fallback

estimatedPrefillTokens = selectionInputTokens

reservedTokens       = blockRoundUp(EstimatedInputHigh + BoundedDecodeTokens)
```

- `selectionInputTokens` 同时用于已有 pressure 下的大/小请求差异，以及 regular aggregate 和
  64K/256K/512K pending-prefill 干扰分层；纯文本使用快速、模型无关的 lexical estimate；
  已识别 multimodal request 使用已有 conservative input estimate，避免把 URL 字符数误当 media
  expansion 成本。两者都不是 exact tokenizer、exact prompt tokens 或 `uncached_tokens`；
- hint 不可用时 `estimatedPrefillTokens` 才回退 `EstimatedInputHigh`，不能因缺失近似信号而
  把长 Prefill 风险当成零；
- `reservedTokens` 用于 post-admit KV operational guard；
- 所有加法和 block rounding 检查 overflow；
- malformed/unsupported 且无法给出 fallback 时 protect；
- 不加载 Hugging Face/vLLM tokenizer，不访问网络/文件/FFI；
- 不记录 prompt/body/model name/request ID 高基数标签。

`max_tokens` 只是 reservation 上界的一部分，不作为短请求排序分数；客户端填写较大
安全上限不应自动被视为长作业。这是有意的第一版取舍：SELECTIVE 比较即时输入大小，
hard-fit 比较完整 bounded reservation。必须额外模拟“小输入 + 大 `max_tokens`”；若它使
QoS 或吞吐劣于基线，才在 release 前调整 selection score，不能现在凭假设增加权重参数。

### 4.3 TPS proxy

相邻两个 snapshot 同 epoch、generation counter 单调且 elapsed 有效时：

```text
aggregateTPSProxy  = generationDelta / elapsedSeconds
runningDenominator = max(previousRunning, currentRunning)
meanActiveTPSProxy = aggregateTPSProxy / runningDenominator
```

该 proxy 是保守的活跃请求平均值，不是真实逐用户 TPS。规则：

- `previousRunning` 与 `currentRunning` 都为零，或 counter 无效：TPS unknown-neutral，不因
  缺样本保护；若请求刚在当前窗口结束，允许用非零的 previous denominator 计算；
- `generationDelta == 0`：TPS unknown-neutral；单个 500-ms prefill/no-progress 窗口不能
  冒充稳定 TPS=0 并触发低流 floor 自锁；hard KV、waiting/KV pressure 和本地 reservation
  仍按正常路径生效；
- preemption/counter reset：本窗口无效并进入相应 hard protection；
- waiting 不使 proxy 失效；waiting 自己会增加 pressure；
- 不保存长历史、不平滑、不学习，也不复用过期 proxy；
- `aggregateTPSProxy` 与 `meanActiveTPSProxy` 必须来自同一个 counter delta，不能通过被保守
  denominator 除过的均值再反推 aggregate；
- Manager 临界区内从 `virtualStateInterval.Upper.DecodeSequences-observedWaiting` 取得
  `effectiveSequences`，并至少由 observed running 封底。fresh sample 的 virtual state 为
  running+waiting，而未被指标吸收的本地 reservation 会额外加一；减去 observed waiting
  后得到正在运行或已本地准入的候选序列。waiting 已单独进入 waitingPressure，不能在 TPS
  denominator 重复收费。这样同一 snapshot 上的后续请求仍能看到前序 admission。

## 5. 最小逐请求算法

所有计算与 reservation add 必须在同一 manager 临界区完成，使同一 poll 内的 burst
看到前序请求占用。

### 5.1 有效状态

```text
effectiveKV = max(managerVirtualPhysicalKVUpper,
                  managerVirtualActiveKVUpper)
postAdmitKV = effectiveKV + requestReservedTokens
remainingKV = hardKVLimit - effectiveKV
```

### 5.2 不可覆盖的保护

以下任一成立立即 `HARD_PROTECT`：

- snapshot/identity/capacity/counter 不可信或 stale；
- preemption 新增或仍在固定 cooldown；
- estimator 无法给出 bounded fallback 或算术 overflow；
- `postAdmitKV > hardKVLimit`；

waiting 本身不进入 `HARD_PROTECT`。这样 `waiting=1` 不会让大小不同的请求全部得到同一
verdict；持续 waiting 会通过 pressure 将 allowance 压小。TPS floor 也不进入
`HARD_PROTECT`：它来自近似均值，只能把选择压力推到最大，不能单独全关。

### 5.3 单一 pressure

三个输入都映射到 `[0,1]`：

```text
kvPressure = clamp(
  (effectiveKV - softKVLimit) /
  (hardKVLimit - softKVLimit), 0, 1)

waitingPressure = waiting / (running + waiting + 1)

projectedTPS = unknown                                    if TPS unknown
projectedTPS = meanActiveTPSProxy                         if TPS >= target, waiting == 0,
                                                            and effectiveSequences == running
projectedTPS = aggregateTPSProxy/(effectiveSequences+1)  otherwise

tpsPressure = 0                                      if projectedTPS unknown or >= target
tpsPressure = (target-projectedTPS)/(target-floor)   if floor < projectedTPS < target
tpsPressure = 1                                      if projectedTPS <= floor

pressure = max(kvPressure, waitingPressure, tpsPressure)
```

这是每次决策重新计算的纯函数，不是持久 controller state。`postAdmitKV` 只用于 hard-fit；
soft KV pressure 使用已经承诺的 current `effectiveKV`，避免把候选 reservation 双重收费。
`effectiveSequences+1` 包含当前候选请求，反馈只更新下一次预测的输入。健康、无 waiting 且
没有本地未吸收 sequence 时允许一次 work-conserving 乐观步；一旦同一 snapshot 已有本地
reservation，后续请求必须进入 post-admit denominator。target 到 floor 之间允许 TPS 下降并
逐渐减少新 intake；projected floor 及以下把输入 allowance 降至零，但
verdict 仍是可自动恢复的 `SIZE_PROTECT`，不是 stale/preemption/KV hard guard。TPS unknown 时
不凭任意 sequence cap 制造 pressure；每个候选仍在同一 Manager 锁内执行 post-admit hard-fit，
并看到前序 reservation 的 KV 占用。

### 5.4 OPEN 与 SELECTIVE

若不可覆盖保护均通过：

```text
if longPrefillGate says protect:
    SIZE_PROTECT
else if pressure == 0:
    ADMIT
else:
    selectiveWindowTokens = hardKVLimit - softKVLimit
    allowanceTokens = min(
        remainingKV,
        floor(selectiveWindowTokens * (1 - pressure)))
    ADMIT        if selectionInputTokens <= allowanceTokens
    SIZE_PROTECT otherwise
```

性质：

- OPEN 不看旧 size 软门槛；普通请求和通过第 5.5 节分层的 idle long request 不因没有 TPS
  样本而自锁；
- 同一 snapshot 下，小输入可能通过、大输入可能保护；
- 压力越高 allowance 单调不增；
- `selectiveWindowTokens` 与 backend KV capacity 同比例缩放，又不会因健康区的全部剩余 KV
  很大而让 waiting/TPS pressure 失效；
- full pressure 的 allowance 为零，阻止极小请求持续穿透 floor；恢复只依赖下一 fresh
  snapshot，不需要固定 hold、probe 或新业务请求解除状态；
- pressure 回落后立即重新计算，不需 learning、probe、promotion 或满 1 秒；
- cold start/zero-delta 不会关闭 intake；其 burst 安全由 hard KV/reservation 和下一 fresh
  snapshot 验证，不以大幅 goodput 退化换取零瞬时 TPS 波动；
- 不保存被拒请求、不实现公平队列；大请求公平性由 large-only/mixed 测试观察。

初始可调参数仅保留：

```text
softKVRatio
hardKVRatio
tpsTarget
tpsFloor
preemptionCooldown
metricsMaxAge
pollInterval = 500 ms
prefillRegularTokens = 65,536
prefillExclusiveTokens = 262,144
prefillQuiescentTokens = 524,288
prefillAggregateBudgetTokens = 262,144
```

KV 参数直接复用明确的 vLLM operational boundaries：`softKVRatio` 对应 VLLM target ratio，
`hardKVRatio` 对应 VLLM hard ratio，不能再像旧 factory 一样把 target ratio 当 hard limit。
TPS target/floor 必须是新 deterministic policy 的显式配置，不得从 TTFT、learner confidence
或探索阈值间接推导。

其中现有 estimator 的 bounded output 配置继续复用，不为本策略增加第二套 output 参数。

### 5.5 独立的 token-weighted 长 Prefill 干扰预测

现有 KV/TPS/waiting pressure 只描述 snapshot 已经观测到的状态。它不能阻止一个 512K/650K
cold prompt 在健康 snapshot 下先进入上游、占用约数十秒 Prefill，再让现有 Decode TPS 在下一次
500-ms poll 后才下降。因此长 Prefill gate 必须在 hard safety guard 之后、`pressure == 0 -> ADMIT`
之前执行，并与请求的 reservation 在同一 Manager 锁内完成 check + reserve。

初始分层使用二进制 K（`64K = 65,536` tokens）：

| `estimatedPrefillTokens` | pre-forward 合同 |
| --- | --- |
| `<64K` | 普通 request-aware admission；没有 pending long Prefill 时，post-admit aggregate pending-prefill tokens 不得超过 256K；已有 exclusive Prefill 时短请求仍按原策略进入；在 512K+ quiescent Prefill 独占阶段仍保护 |
| `64K–<256K` | post-admit aggregate pending-prefill tokens 不得超过 256K；aggregate 包含所有尚未 prefill-complete 的本地 reservation，防止多个 63K/100K 组合绕过成本预算 |
| `256K–<512K` | 每副本最多一个该级或更大的 pending Prefill；普通 `<64K` 请求仍可按原策略进入，以免用一个长请求把短请求 goodput 全部清零 |
| `>=512K`（含 650K） | 仅在 observed running=0、waiting=0、本地 effective sequences=0、所有本地 pending Prefill=0 时允许首个请求；从 reserve 到 prefill-complete/terminal 为独占 Prefill 阶段，期间任何新请求都 pre-forward `SIZE_PROTECT` |

关键语义：

- 分层依据快速、模型无关的 `estimatedPrefillTokens`：纯文本用 lexical hint，已识别 modality
  用 existing conservative input estimate，hint 缺失时回退 `EstimatedInputHigh`。它不宣称知道
  exact tokenizer token、cache-cold 或 uncached token 数；
- hard KV safety 始终独立使用 `EstimatedInputHigh + BoundedDecodeTokens` 的 block-aligned
  reservation，绝不能用通常更小的 lexical estimate 放松 hard-fit；
- `pending` 从成功原子 reservation 开始，而不是等 vLLM metrics 看见请求；这样同一 snapshot 的
  并发 burst 不能穿透；forward failure、cancel、disconnect、timeout、error 和 terminal 删除，
  首个有效 semantic response 调用 `MarkPrefillComplete`，均复用同一 exact-once 生命周期；
- terminal 立即删除本地 pending/独占状态，但不能把仍显示 `running>0` 的上一份 vLLM snapshot
  虚构成 idle；因此取消后的另一个 512K+ 请求在旧 snapshot 下继续保护，fresh snapshot 确认
  `running=0` 后立即恢复，恢复延迟上界为一个默认 500-ms poll。`MarkPrefillComplete` 已确认
  Prefill 阶段结束，因此后续 `<512K` 请求不等待 poll 即可退出独占阶段；但该请求仍处于
  Decode/local reservation 时，`effective sequences>0` 必须继续阻止第二个 512K+ 请求；
- 没有 pending long Prefill 时，`<64K` regular 与 `64K–<256K` weighted 都受同一个 256K
  aggregate budget；精确 256K 允许，超过才保护。已有 exclusive Prefill 时仍允许普通短请求，
  避免低流自锁；512K+ quiescent 阶段继续保护所有新请求；
- 256K–<512K 的“一次一个”指该级或更大的长 Prefill，不禁止普通短请求；其真实 QoS/goodput
  权衡必须由 simulation 和 shadow/canary 证明，不能仅由规则直觉晋级；
- 512K+ 独占阶段必须能被 Router inspect 看见，但 inspect 不创建 reservation、不刷新阶段、
  不把最后一次拒绝保存为 sticky global block；prefill-complete 或 terminal 后下一次 scrape 自动恢复；
- PIG restart 后无法区分已经 running 的请求处于 Prefill 还是 Decode，统一把 observed running 当作
  需要保护的 active work；因此不误放 512K+，待 backend 真正 idle 后自动恢复；
- 先保留 token 预算，不引入 `estimated_prefill_seconds`/模型 profile/p10 learner。若实际多卡模型
  证明 token 数不足以表达 Prefill 成本，再以独立计划增加可验证的 model-agnostic rate input；
- 阈值和 64K–256K aggregate budget 是显式、有序、带默认值的 bounded config，默认分别为
  `65,536 / 262,144 / 524,288 / 262,144`；不得按模型名硬编码，invalid ordering 启动即失败。

该 gate 的目标不是拒绝所有大请求，而是让 64K、256K、512K、650K 在同一时刻得到不同决策，
把最危险的长 Prefill 移到真实空闲窗口，同时保持 `<64K` 与低流首个长请求 work-conserving。

## 6. Reservation 与真实 HTTP 路径

第一版复用并精简现有 `Manager`，不重写第二套 reservation 系统：

```text
request parse/estimate
  -> fresh snapshot + existing reservations
  -> pure policy verdict
  -> atomic reserve on ADMIT
  -> enforce 转发到已配置的单一 upstream 并原子 mark-forwarded；不路由、不进入旧 QoS queue/tier/priority
  -> prefill/reconcile or terminal exact-once release
```

必须验证现有 Manager：

- 同一锁内 check + reserve；
- duplicate request 不重复 reserve；
- forward/prefill/sample 的 ambiguous window 不提前复用容量；
- pending-prefill tokens、长 Prefill count 与 512K+ 独占阶段和 reservation 原子变化，不能依赖
  500-ms sample 才生效；
- completion/error/cancel/disconnect/timeout/reset/shutdown exact-once；
- reservation 不 leak、不 double release、不 oversubscribe；
- 去掉 SchedulerObserver、learning invalidator、outcome training 后，上述资源语义不变；
- shadow 只计算 would-verdict，不修改 enforce reservation 或下一次决策。

同 identity/capacity/block 的 counter reset 走单一 Manager epoch-rebase 操作：在 Manager 锁内
关闭 intake、清空旧 reservation/retired state、替换 base、推进 event sequence，再 reopen；随后
observer 才发布 reset sample 为 fresh baseline。任何旧 reservation handle 的 forward commit 必须
失败，terminal 可幂等返回；identity/capacity/block drift 不走该恢复路径。

若现有 Manager 的 learning coupling 无法在小 diff 内拆除，再在更新本文档后决定重写；
不能仅因代码复杂就并行保留两个 manager。

## 7. SOLID 边界

只保留有实际职责的五部分：

- existing `RequestCostEstimator`：bounded 请求估计；
- existing `VLLMObserver`：immutable snapshot、epoch、500-ms counter delta；
- new `RequestAwarePolicy`：无锁纯函数，输入 state/cost/config，输出 immutable verdict；
- existing `Manager`：原子 check/reserve、epoch 与 terminal lifecycle；
- thin transport/reporting adapter：把同一个 immutable verdict 映射到 HTTP、日志、metrics 和
  Router-readable capacity，不拥有第二套 resource policy 或 reservation registry。

不为每个函数创建接口；仅在需要替换实现或隔离外部 I/O 时使用接口。domain policy 不依赖
HTTP、Prometheus、Router、Docker 或 tokenizer asset。

## 8. 日志、metrics 与 Router contract

每个真实请求的同一个 immutable verdict 至少包含：

```text
action = admit|size_protect|hard_protect
reason
pressureSource
pressureValue
selectionInputTokens
estimatedPrefillTokens/prefillClass
pendingPrefillSequences/pendingPrefillTokens/postAdmitPendingPrefillTokens
pendingLongPrefillSequences/pendingQuiescentPrefillSequences
reservedTokens
allowanceTokens
effectiveKV
postAdmitKV
remainingKV
running/waiting/effectiveSequences/reservedCount
aggregateTPSProxy/projectedMeanActiveTPSProxy
```

- 数值用 gauge/histogram，不作为 label；
- label 只允许低基数 action/reason/source；
- last-decision HTTP、日志和 metrics 必须来自上述同一个真实请求 verdict，不得各自重算；
- 名为 `pending_*` 或无前缀 `post_admit_pending_*` 的 operational gauge 必须表示当前 Manager lifecycle state，reservation drain 后
  无需等待新请求即清零；last-decision pending/post-admit 值使用显式
  `last_decision_pending_*` 名称保留，不能再次用 current-looking 名称发布 stale value；
- Router-readable capacity 是单独的当前状态投影：每次 metrics scrape 使用相同 pure policy
  和当前 Manager virtual state 无副作用地评估 one-block inspect，不写 reservation、不增加
  request/attempt 计数，也不覆盖 last-decision metrics；
- one-block inspect 为 `HARD_PROTECT` 时发布 `inspectCapacity=0`，并令有效 global limit 等于
  有效 running，即可用容量为零；不是把 Router 的 global-limit 数值字面写成零；
- one-block inspect 为 `ADMIT` 且 `pressure>0` 时发布 bounded `inspectCapacity=1`，使后续
  请求仍可到 PIG 做大小判断；不能因为拒绝一个大请求就阻止之后的小请求到达；
- one-block inspect 为 OPEN 时立即撤销 Router clamp；不等待新业务请求或固定 hold，防止
  低流/恢复自锁；shadow 始终不改变 Router capacity；
- enforce 的 Router projection 不继承旧 dynamic controller 的 effective waiting/global-limit
  判定：新增 raw waiting 仅供观测，Router 消费的既有 waiting 为零；OPEN effective global
  limit 为非正的 neutral sentinel，SELECTIVE/HARD 才使用 `effectiveRunning+1`/
  `effectiveRunning`。raw global limit 继续单独暴露，不能重新进入 Router verdict；
- HTTP 必须实际保护，不能只记 `would_protect` 后仍 forward；
- Router 继续发送另一个请求到 SELECTIVE PIG 是逐请求策略需要，不等于 PIG 在路由。

Router capacity 的具体既有字段只做适配，不修改 Router 源码。

## 9. Test-first 顺序

### 9.1 第一组 pure-policy behavioral red

先只覆盖最小因果合同：

1. 同一 snapshot 下 small `ADMIT`、large `SIZE_PROTECT`；
2. 同一个 `<64K` large fixture 或满足第 5.5 节分层的 idle long request 在 OPEN hard-fit 时
   `ADMIT`；
3. `waiting=1` 进入 SELECTIVE，小请求仍可能通过；
4. TPS 从 target 到 floor 时 allowance 单调收缩，floor 为最大 SELECTIVE 压力且 allowance
   为零；下一 fresh snapshot 恢复后重新计算，不产生持久 TPS-only 全局锁；
5. 健康 snapshot 的第一个 work-conserving 请求可进入，但同一 snapshot 的后续 burst 必须
   随 `effectiveSequences` 增加而降低 projected TPS，不能无限复用当前均值；
6. TPS unknown 不凭任意 sequence cap 限流，但每个候选仍看到前序 reservation 并执行
   post-admit hard KV；
7. fresh 完成窗口即使携带 TPS proxy，只要当前没有 running/local effective sequence，就不得
   形成低流 hard/size lock；第一个请求进入后，同 snapshot 后续请求仍可使用 post-admit TPS；
8. hard KV、preemption、stale snapshot 不能被小请求覆盖；
9. 不同历史/调用顺序不会改变相同 pure input 的 verdict；
10. soft/hard KV limits 必须向下 block-align，且 hard-fit、allowance、telemetry 共用同一边界。
11. pure policy config 独立拒绝 `soft<=0`、`hard>=1`，不能依赖 production loader 才守住
    `0 < soft < hard < 1`。
12. 同一 healthy/pressure=0 snapshot 且已有 Decode 时，650K 不得再直接 OPEN；backend 完全
    idle 时首个 650K 可进入，精确的 512K boundary 属于同一层；
13. `<64K` 保持普通 admission；`64K–<256K` 使用 post-admit 256K aggregate pending-token
    budget；`256K–<512K` 最多一个同级或更大 pending Prefill；
14. 512K+ reservation 后的独占 Prefill 阶段保护后续 small/medium/long；prefill-complete 后
    `<512K` 立即恢复，但第二个 512K+ 在 local effective sequence/observed running 清零前仍保护；
    terminal 立即释放本地 gate，但另一个 512K+ 仍等待 fresh idle snapshot；
15. 64K/256K/512K 边界前后各一个 token、650K、overflow 和 invalid threshold ordering 全覆盖。
16. regular-only aggregate 在精确 256K 仍 ADMIT、超过一个 token 即 pre-forward protect；同一
    exclusive Prefill 后的普通短请求仍 ADMIT，避免把长请求预算变成全局锁。
17. 同一 multimodal request 保持 hard-KV safety upper 不变，但把已识别 modality 的 conservative
    input estimate 送入真实 pre-forward Prefill admission；纯文本 lexical hint 语义不变。

red 必须 compile，并因 stub/旧行为不满足上述语义而失败；不能用缺依赖或语法错误冒充。

### 9.2 Manager/HTTP integration

- burst 请求按前序 reservation 重算；
- cancel/error/timeout/disconnect/reset/shutdown exact-once；
- sample reconcile 不提前释放或永久保留 reservation；
- 同一 snapshot 的 1/16/64/256 并发 long burst 只能按 token budget/长 Prefill count 原子通过；
- `MarkPrefillComplete`、completion-before-next-poll、forward failure、cancel、disconnect、error、
  timeout、shutdown 和 epoch rebase 精确释放本地长 Prefill reservation，重复 callback 不
  double-release；terminal 不覆盖仍为 busy 的观测，取消后的 512K+ 重试须在下一份 fresh idle
  snapshot（默认不超过 500 ms）恢复；
- low-flow、idle 首个 512K/650K、transient waiting、stale recovery 无自锁；
- metrics scrape 的 one-block inspect 可在没有新业务请求时从 HARD/SELECTIVE 恢复 OPEN，
  且 probe 不写 reservation、不增加业务 attempt；
- 同 identity/capacity/block counter reset 原子 rebase 后自动恢复；identity/capacity/block drift
  仍 fail closed；旧 reservation 不能在 rebase 后 forward，且没有 reservation/retired leak；
- startup probe 作为第一份 TPS counter baseline，第一轮 poll 可直接形成有效 delta；
- `previousRunning>0 -> currentRunning=0` 的 fresh completion snapshot 不得在 observer→adapter
  真实决策路径误锁下一请求；
- shadow 无副作用；
- HTTP/log/metrics/Router action/reason 一致；
- 当前 pending metrics 在 reservation/prefill/terminal 生命周期中变化，drain 后即为零；显式
  last-decision metrics 继续保留最后 verdict，二者不能互相覆盖；
- enforce policy-fit 请求不再被旧 static/dynamic/tier gate 二次拒绝或排队，也不注入 backend
  priority；shadow 仍保持原服务路径；
- enforce Router projection 在 raw dynamic waiting/limit 改变时仍只由 request-aware
  OPEN/SELECTIVE/HARD 决定；SELECTIVE 必须至少允许一个后续请求到达 PIG，OPEN 必须在无新
  业务请求时立即解除外层 clamp；
- enforce 不因不可达的 backend-priority-only 配置值失败，shadow/off 仍拒绝无效 priority
  配置；OpenAI compatibility config 继续独立验证；
- enforce `/v1/upstream-status` 与同一 fresh request-aware inspect 一致：OPEN/SELECTIVE/HARD
  分别 green/yellow/red，且 raw old dynamic/queue/tier 不能改变结果；
- 旧 learner/config/factory 不再影响 verdict。
- 旧 Router hold env/config 不再影响 request-aware Load/Validate/startup 或 Router projection。

### 9.3 Builder gates

仅在授权 builder 运行：

- focused/package/full `go test`；
- `gofmt -d`、`go vet`、`go test -race`；
- estimator 1 KiB/64 KiB/2 MiB benchmark；
- policy hot path benchmark，目标 O(1)、0 alloc；
- 1/16/64/256 并发 reservation race/lock test。

本地 Windows 只做源码检查、`apply_patch` 编辑、归档、hash 和 Git 操作。

## 10. 确定性 simulation

simulation 必须调用生产 `RequestAwarePolicy`，固定 seed，可重放。比较：

1. 简单全局 KV/waiting/TPS 二元拒绝；
2. request-aware policy。

场景：

- short-only、large-only；
- 64K/256K/512K boundary 与 650K，多卡大上下文 capacity 下单独验证；
- 80/20、50/50、20/80 small/large mixed；
- 多个 short/medium pending 与一个 256K/512K/650K 的到达顺序组合；
- existing Decode + long Prefill、idle first 650K、simultaneous long burst、cancel during Prefill、
  cancel 后旧 busy snapshot 继续保护且下一 poll 恢复、prefill-complete-before-poll、512K+
  独占阶段与恢复；
- 小后大、大后小；
- 500-ms poll 前 burst；
- 28 background Decode 下同一 100-ms tick 的 40 个 regular multimodal Prefill；global-binary
  baseline 允许 40 个，candidate 在 256K aggregate 边界只允许 32 个并保护 8 个，TPS-floor/
  waiting 不得比 baseline 更差；
- low-flow、idle 首个 512K/650K、transient/sustained waiting；
- 小输入 + 大 `max_tokens`、大输入 + 小 `max_tokens`；
- KV 低/中/高、TPS target/floor、preemption、stale/recovery；
- cancel、短 completion、长 streaming。

TPS target/floor 场景必须通过改变合成 backend 的真实 aggregate capacity 与 background
sequences 产生，observer proxy 必须继续由真实 generation delta 计算。不得用与 engine ground
truth 脱离的固定 TPS override 覆盖观察值；否则会人为遮蔽 admission 后的 QoS 退化，不能作为
算法通过或失败的证据。

第一轮只比较少量预注册配置，不做在线 learner 式大规模调参。晋级要求：

- preemption 不高于基线；
- 每个场景和 suite aggregate 的 TPS floor violation duration 最多比基线多一个 100-ms
  simulation tick；这是预注册的轻微 QoS 下降预算，不得按场景累加放大；每个场景和 suite
  aggregate waiting duration 同样不能把逐场景容差累加；
- short-only 和 large-only 无持续 idle/self-lock；
- duration 比较使用一个 tick budget 加极小 seconds epsilon；goodput 比较只使用相对阈值加
  极小 tokens/s epsilon，不能复用 duration tolerance；
- uniform、low-flow、burst 与 output-horizon 场景的 total/SLO completion tokens/s 均不得比基线
  退化超过 1%；不能以相同 QoS 下少处理大请求冒充保护成功；
- mixed SLO-compliant completion tokens/s 或 total completion tokens/s 有可复现提升，
  另一项不退化；
- 两种到达顺序都通过；
- 若改善落在测量噪声内，只能结论“未证明更好”。

真实单用户 TPS、GPU utilization 和 completed requests/s 在 shadow/canary 继续验证，不能
由 pure-policy benchmark 推断。

## 11. 三轮实现复查

每个 release candidate 记录 exact archive/source SHA-256、builder、命令、退出码：

1. **因果**：request size 是否改变真实 pre-forward verdict；OPEN 是否 work-conserving；
2. **安全**：KV margin、reservation、burst、preemption、stale/reset、低流/race；
3. **SOLID/证据**：旧 learning 是否删除；HTTP/log/metrics/Router 是否同 verdict；
   focused/full/image/deploy/live 证据是否分层。

发现问题先更新本文档，再补 red 和修复。

## 12. 版本与发布流程

1. canonical plan；
2. pure-policy behavioral red/green；
3. Manager/HTTP integration red/green；
4. focused deterministic simulation；
5. reservation/Close、Router inspect 与 legacy wiring 审计及 red/green；
6. 三轮实现复查，其中先完成 production reachability 审计并删除 v0.11 可达的旧 predictive
   learner/calibrator/TTFT config/metrics 消费；保留其他模式仍使用的公共 dynamic/KV 配置；
7. 上述审计不再产生源码变更后，封存唯一 exact-source archive，在同一 archive 上执行
   full/vet/race/build/benchmark/simulation 最终矩阵；
8. 自主管理 patch version，完成 runtime/README/OCI release identity 后证明除 release identity 与
   ledger 外没有 executable drift；
9. commit/push/annotated tag；任何旧版本 source/image evidence 均不得冒充当前 release evidence；
10. builder 构建并发布 immutable digest 镜像；
11. 仅 `use1-cb`、Router disabled shadow；
12. 仅 `use1-cb`、Router disabled 短时 enforce；
13. readiness、代表请求、日志、metrics、无 crash/restart/preemption 后，才 enable
    Router `use1-cb`；
14. 观察实际流量 30 分钟，与旧版 `use1-4c` 的相近负载窗口分开对照；
15. 有明显问题则 disable `use1-cb`、更新计划、bump patch version 并重新循环。

v0.11.3 已完成 source/image/shadow/enforce，但 Router canary 发生 Decode freeze，明确失败并回退，
禁止重新启用。v0.11.4 corrective 当前只完成 source candidate 的 behavioral red/green 与 builder
matrix；必须完成三轮复查、release identity、commit/tag/image、Router-disabled shadow/enforce 后，
才可重新讨论 Router canary，不能继承 v0.11.3 的 live promotion 结论。

没有明确 GO 前不得修改其他 CVM、修改 Router/vLLM 源码或引入生产流量。

## 13. 当前进度与证据边界

- [x] 无学习、request-aware deterministic 方向确认；
- [x] 对计划完成第五轮合理性/过度设计复查并精简；
- [x] 默认 poll interval 为 500 ms；
- [x] cache、TTFT、tier、priority、routing 不参与 admission；
- [x] pure-policy behavioral red；
- [x] pure-policy implementation/focused green；
- [x] Manager atomic reservation integration；
- [x] HTTP adapter with static snapshot integration；
- [x] production observer request-aware snapshot；
- [x] deterministic config/factory integration；
- [x] request-aware last-verdict metrics、non-sticky Router effective waiting/inspect capacity 与
  predictive coarse upstream status；
- [x] 真实 proxy HTTP、统一 verdict 日志与恢复路径 integration；
- [x] 删除 dirty v0.11 learner/Safe-Envelope/pressure-bucket 实验并清理 production legacy
  factory/config/metrics 消费；
- [x] R92 unknown-TPS single-speculation focused green，但被 R93 burst goodput gate 否决；R94
  补齐 acceptance red，当前源码已回退该规则并由 R95 simulation green 覆盖；
- [x] startup TPS baseline、same-identity counter-reset rebase 与 dead Router-hold config
  focused/package/race red/green；
- [x] R96 concurrent rebase 1/16/64/256、count=10 与 focused race；
- [x] R97/R98 fresh completion-window low-flow mislock red/green；
- [x] R100/R101 pure-policy KV band/margin validation red/green；
- [x] R102 exact executable-source full/vet/full-race/build/benchmark；
- [x] aggregate QoS budget、healthy-burst forecast、block-aligned limit 与 acceptance 单位修正
  focused red/green；
- [x] R86 deterministic simulation/benchmark（已被 R88 source change supersede，不能作最终证据）；
- [x] R95 corrected deterministic simulation（只证明 synthetic exact archive，已被后续 test/doc
  变化 supersede，不能作最终 release evidence）；
- [x] 第六轮三轮实现复查完成；
- [x] R102 exact executable-source deterministic simulation/replay；
- [x] commit/push/tag；
- [x] R103 builder-local image、registry immutable digest 与 production image contract；
- [x] `use1-cb` Router-disabled shadow deployment/readiness/protocol/low-flow gates；
- [x] v0.11.0 `use1-cb` Router-disabled enforce deployment/readiness（仅隔离环境，不晋级）；
- [x] v0.11.1 64K/256K/512K/650K behavioral red/green、lifecycle、telemetry 与 simulation；
- [x] v0.11.1 exact-source clean-builder matrix；
- [x] v0.11.1 source commit/push/annotated tag，但发布前发现 Dockerfile OCI label 仍为
  `0.11.0`，因此该 tag 明确禁止构建、发布或部署；
- [x] v0.11.2 version/OCI-label corrective、exact-source builder matrix、tag/image 与
  Router-disabled shadow；其 live 分层尺度错误已由 v0.11.3 corrective 取代，不得晋级；
- [x] v0.11.3 lexical Prefill interference estimate 与 hard-KV safety upper 分离、三轮代码复查、
  exact-source builder matrix、commit/push/annotated tag、clean-tag immutable image 与 provenance；
- [x] v0.11.3 `use1-cb` Router-disabled shadow deployment/readiness/protocol/low-flow/size gates；
- [x] v0.11.3 `use1-cb` Router-disabled enforce deployment/readiness、weighted aggregate budget、
  low-flow/cancel/burst、约 230K exclusive 与 final-preflight gates；
- [x] v0.11.3 Router canary 已执行、发现约 80 秒 Decode freeze，并安全 disable `use1-cb`；该版本
  production promotion 明确失败；
- [x] v0.11.4 regular multimodal burst、current-vs-last telemetry behavioral red；
- [x] v0.11.4 source corrective、focused green 与 exact candidate full/vet/build/race/
  byte-identical simulation/benchmark matrix；
- [x] exact 512K boundary 与 650K idle/busy/cancel/recovery 继续作为通用多卡合同，由 exact-source
  builder tests 和 deterministic multi-card simulation 覆盖；当前 262K 节点未实发 512K/650K；
- [x] v0.11.4 三遍最终复查；第二遍发现并修正无前缀 post-admit gauge 的 stale 语义，R130
  behavioral red/focused/full builder matrix 已通过；
- [x] v0.11.4 runtime/README/OCI release identity 与 exact-source builder matrix；
- [x] v0.11.4 source commit/push/annotated tag、clean-tag builder-local image、registry immutable
  digest、production image contract、runtime version 与 local/registry binary provenance；
- [x] GitHub Publish Image #26 的红状态继续如实保留；artifact 已发布并逐项验证，用户在确认近期
  registry publication 实际由 tag-triggered CI 完成、builder 只做独立构建/回拉验证后，于 2026-08-07
  指示继续。该 terminal HEAD anomaly 作为 v0.11.4 一次性非阻断 release-process 记录关闭；不重跑、
  不重推、不移动 tag，也不把该关闭外推为 deployment/readiness/canary green；
- [x] Publish Image #26 raw job log、`.dockerbuild` artifact 与 R133 registry image/config/binary
  exact cross-provenance；重跑会重新构造含 build timestamp 的 image config，禁止把重跑当无副作用读操作；
- [x] v0.11.4 部署前只读 live drift audit、精确 rollback 与 shadow/enforce candidate；未部署、未改
  Router、未发送推理请求；
- [x] v0.11.4 Router-disabled preflight/shadow/enforce 专用 harness 编写、三遍复查与 PowerShell
  静态语法/无生产写命令审计；harness 尚未运行，不构成 deployment/readiness 证据；
- [x] v0.11.4 30 分钟 canary observer、只摘 `use1-cb` 的 exact-once stop rollback、drain 与
  只读 progress reader 三遍静态复查；observer 尚未运行，不构成 canary/production 证据；
- [ ] v0.11.4 Router-disabled shadow/enforce 与 Router enable 前 current-state drift recheck；
- [ ] v0.11.4 Router canary 与 30 分钟实际流量观察。

截至当前，v0.11.4 已发布为 immutable registry image
`ghcr.io/phala-network/phala-inference-guard@sha256:b8756c49271d7ac0c42f46cd0201db571cd02bce1c08e3721fafe8ae0a2e016e`；
clean-tag source、builder-local binary 与 registry binary provenance 已闭合。GitHub workflow #26 在完成
production contract、build、layer/manifest push 后因 manifest HEAD `denied` 被标红，虽不否定已验证 artifact，
仍如实记录为发布流程异常；用户已在 R138 指示按一次性非阻断记录继续，不改变 workflow 的客观 failure，
也不允许重跑或移动 tag。`use1-cb` 已 Router disabled；v0.11.4 没有
Compose、部署、readiness 或实际流量证据。任何 v0.11.3 shadow/enforce green、零 preemption，或 v0.11.4
builder/image green 都不得冒充 v0.11.4 production readiness。

### 13.1 R19 pure-policy behavioral red

```text
archive:
  tmp/pig-v011-request-aware-r19-red.tar.gz
SHA-256:
  F85C9E684E5EB64DC86726BC911DDC939211B77286BC44A051C6B9E32B959CC6
builder:
  cvm_3e2k83KX / pig-v01011-builder / Go 1.24.13 linux/amd64
commands:
  gofmt -d request_aware_policy.go request_aware_policy_test.go
  go test ./internal/runtime/predictive -run TestRequestAwarePolicy -count=1 -v
```

`gofmt -d` 无差异，package 编译成功。六组合同中五组因 stub 始终返回
`hard_protect/invalid` 而失败：small/large selection、OPEN large、waiting selective、
TPS continuous/floor、hard reason priority。history-independence 通过，因为 stub 本身无状态。
这是有效行为 red，不是编译、依赖、格式或 runner 失败。

R18 仅发现 `gofmt -d` 差异，测试未运行；更早一次 R18 调用还发生远端引号错误。二者均
不是 behavioral red，不能作为算法证据。

### 13.2 R20 pure-policy focused green

```text
archive:
  tmp/pig-v011-request-aware-r20-green.tar.gz
SHA-256:
  B98B342E5097C8AE7601A6C44D4A78A05B26A11112BD1E33291E7D87B919C2DD
builder:
  cvm_3e2k83KX / pig-v01011-builder / Go 1.24.13 linux/amd64
commands:
  gofmt -d request_aware_policy.go request_aware_policy_test.go
  go test ./internal/runtime/predictive -run TestRequestAwarePolicy -count=1 -v
result:
  PASS, six behavioral groups, package compiled, 0.002 s
```

该归档曾证明 pure policy 的 small/large、OPEN、waiting selective、旧 TPS hard floor、
hard guard priority 和 history-independence。本轮复查已废弃旧 hard-floor 语义，因此 R20
不再是当前 policy 的完整 green；Manager/adapter 的结构性证据仍需在新 policy green 后重跑
适用测试。不能称为可部署。

### 13.3 R21 Manager behavioral red

```text
archive:
  tmp/pig-v011-request-aware-r21-manager-red.tar.gz
SHA-256:
  5050A53BAF2910243C620AC933C28B5EEF881D298241EE698455DB22A6114DCC
builder command:
  go test ./internal/runtime/predictive -run TestRequestAwareManager -count=1 -v
```

`gofmt -d`、package compile 均成功。真实 reservation causality、burst 重算、既有 terminal
lifecycle 三组测试均因可编译 Manager stub 固定返回 `hard_protect/unavailable` 而失败，是
有效行为 red。它尚未证明 HTTP adapter 已接入。

### 13.4 R22 Manager focused/package/race green

```text
archive:
  tmp/pig-v011-request-aware-r22-manager-green.tar.gz
SHA-256:
  CE4197E2DF64E922F0006635B921389A743E79C16B6C7827BD329B84EEC90BE1
focused:
  go test ./internal/runtime/predictive -run TestRequestAware -count=1 -v
  PASS, 0.003 s
package:
  go test ./internal/runtime/predictive -count=1
  PASS, 0.038 s
  log SHA-256 0AC6EA1C2416C7C1ADD127E084D9D7618BE659F9E15973089109189C00D0C550
focused race:
  go test -race ./internal/runtime/predictive -run TestRequestAware -count=1
  PASS, 1.017 s
  log SHA-256 1CB12106CEC94367626CCB22241E55F6D4F51111B8970C3CD0BF898CFC6B5530
```

新入口在现有 Manager 锁内读取 `virtualStateIntervalLocked().Upper`，调用 pure policy，
仅在 `ADMIT` 时写入既有 reservation map；MarkForwarded/Terminate 复用原 lifecycle。没有
新 manager、learner 或历史状态。focused race 仅证明当前 focused 测试未发现 race，尚未
覆盖真正并发 burst；HTTP/observer/factory、full repo、benchmark、simulation 仍未验证。

### 13.5 R24 server adapter behavioral red

```text
archive:
  tmp/pig-v011-request-aware-r24-adapter-red.tar.gz
SHA-256:
  F5348E309C9C69F96DDBCD0D4A522DC06FB4643BB318CD40C00743EAE8A8AC29
builder command:
  go test ./internal/app/server -run TestRequestAwareAdapter -count=1 -v
```

`gofmt -d`、server package compile 成功。request-size pre-forward causality、OPEN large、
waiting selective 和 reservation lifecycle 四组测试均因 adapter stub 固定返回 availability
protection 而失败，是有效 behavior red。R23 只发现格式差异、未运行测试，不是 red。

### 13.6 R25 server adapter focused/package/race green

```text
archive:
  tmp/pig-v011-request-aware-r25-adapter-green.tar.gz
SHA-256:
  2FCE7E769912770471ADE69EAFA6F0D5F1C6A0FC64F6A319BCF9455B3F650732
focused:
  go test ./internal/app/server -run TestRequestAwareAdapter -count=1 -v
  PASS, 0.006 s
server package:
  go test ./internal/app/server -count=1
  PASS, 1.935 s
  log SHA-256 901F00064A30B4BDEAA1E83139A27A2410476FDF139D62679C855F52BC9F6236
focused race:
  go test -race ./internal/app/server -run TestRequestAwareAdapter -count=1
  PASS, 1.020 s
  log SHA-256 D8E93DD37DF647EE932141F321F9C7D5D9AE7225288ED3C8F138E7E1A3EC5F2B
```

已证明 bounded request cost 在 server adapter 的 `Decide` 中实际改变 pre-forward outcome，
并且 ADMIT 使用现有 Manager reservation/lifecycle。当前 snapshot 仍是测试静态 provider；
factory 仍实例化旧 learning adapter，生产路径尚未切换。

### 13.7 R26 observer behavioral red

```text
archive:
  tmp/pig-v011-request-aware-r26-observer-red.tar.gz
SHA-256:
  F1FA2BDD4CC65692B1CD6A6FCD19A4583535000D6C173AC20F4E6C9A854DFCFD
builder command:
  go test ./internal/app/server -run TestRequestAwareObserver -count=1 -v
```

server package 编译成功，fresh capacity/running、waiting 下的 500-ms TPS、zero-generation
unknown、preemption cooldown 与 freshness 三组测试均因可编译 observer stub 返回空 snapshot
而失败，是有效行为 red。

### 13.8 R27/R28 TPS floor 合同修正 red/green

第二轮计划复查发现旧 hard floor 会让一个近似 500-ms 样本全局关闭 intake，因此废弃 R20
中的旧 floor 语义并重开证据：

```text
R27 red archive:
  tmp/pig-v011-request-aware-r27-policy-floor-red.tar.gz
SHA-256:
  8C2D6F1A2C461983716277DFB846505E3D2B7D94000E96DBE4894C618A9C4589
behavioral-red log SHA-256:
  25081979837EEA818EFF4CA0B987710592AC0809FE92614F030434808763883C
command:
  go test ./internal/runtime/predictive
    -run TestRequestAwarePolicyTPSContinuouslyShrinksAllowanceWithoutGlobalFloorLock
    -count=1 -v

R28 green archive:
  tmp/pig-v011-request-aware-r28-policy-floor-green.tar.gz
SHA-256:
  15619E8C7278D7EF3C8CA5F7BDEDAC6215EA854CA37E4C783532F341BDC5436B
focused/package log SHA-256:
  0EBCE3F42B2AF57526D1469444FB86DA6ACAF33101F58F5B4D964DB2DE058237
commands:
  go test ./internal/runtime/predictive -run TestRequestAwarePolicy -count=1 -v
  go test ./internal/runtime/predictive -count=1
result:
  PASS, 0.002 s / PASS, 0.039 s
```

R27 在 target/midway 正确、但 floor/below-floor/tiny 仍返回 `hard_protect/tps_floor`，失败
原因精确命中新合同。R28 删除 TPS-only hard branch 后，floor/below-floor 为最大 SELECTIVE
pressure，普通请求保护而一个 block 的 tiny hard-fit 请求可进入。R28 `gofmt -d` 为空，空文件
SHA-256 为 `E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855`。

第一次 R27 builder 调用只因非登录 shell 找不到 `gofmt` 而停止，不是行为 red；随后对同一
archive SHA 使用 `/usr/local/go/bin/{gofmt,go}` 重跑，才得到上述有效 red。

### 13.9 R29 observer focused/package/race green

```text
archive:
  tmp/pig-v011-request-aware-r29-observer-green.tar.gz
SHA-256:
  7FB9E3F371CE74AF595460AC5DDA2B159929EF95EDBB799B4B93335F7BD829C7
commands:
  go test ./internal/app/server -run TestRequestAwareObserver -count=1 -v
  go test ./internal/app/server -count=1
  go test -race ./internal/app/server -run TestRequestAwareObserver -count=1
result:
  PASS, 0.015 s / PASS, 1.945 s / PASS, 1.035 s
combined log SHA-256:
  CAC58AC9799347D038054AFCA652F5A0F77C08F18E6908C0AF5DBE8251A5C74D
```

observer 只发布最近一次成功 reconcile 的 immutable state；TPS 只由相邻成功 snapshot 计算；
waiting 不使 TPS 失效；zero generation 为 unknown；preemption 在 reconcile 前先使旧 snapshot
失效，并由 getter 发布 cooldown；counter/identity reset 清空 request-aware state。`gofmt -d`
为空。尚未证明 factory/真实 proxy HTTP 使用该 snapshot，不能称为生产路径完成。

### 13.10 R30-R32 deterministic TPS config red/green

```text
R30 loader red archive/SHA-256:
  tmp/pig-v011-request-aware-r30-config-red.tar.gz
  739853B4014233E16947B1F238C0EE5054FE57784D520DF9EB89CE1A1C393572
R30 log SHA-256:
  2D91848870C311AE149D44FA65D2C94FEF95045723434F152EDA6A170AB27192
R31 bounds red archive/SHA-256:
  tmp/pig-v011-request-aware-r31-config-bounds-red.tar.gz
  AC38B9A3F692E432B165A3CF13921D936C9E331532E0AF52A844EF3A0013462E
R31 log SHA-256:
  A49585CB37200D3E3665FE9D9BC2B1EBFC6A19BCD0B89B2F95F79ADB5A388E30
R32 green archive/SHA-256:
  tmp/pig-v011-request-aware-r32-config-green.tar.gz
  44413687C7EBAE807800CE488402ED92DE0F51EE985D7184E934E7BEBD1871C3
R32 log SHA-256:
  EB20670E9E437DDEB5B69F7374939EBB6C85867EE4A5D7EEEDB5B0E933FD3303
```

R30/R31 精确证明新字段此前未加载、默认/override 为零，且 `floor >= target` 未被拒绝；
R32 后 `PREDICTIVE_TPS_TARGET=25`、`PREDICTIVE_TPS_FLOOR=20` 的默认、override 和 bounds
均通过，pigconfig package 通过，`gofmt -d` 为空。

### 13.11 R33-R35 deterministic factory red/green

```text
R33 behavioral red archive/SHA-256:
  tmp/pig-v011-request-aware-r33-factory-red.tar.gz
  54B2E85A564CF539FAEFA437738CE9C6E41B9425D4C3EFCA03B91DC8BE9BA6A9
R33 log SHA-256:
  507DBE9CB56B65596F509F6C7E6A299276D09D61FE2A992BF59361013D457B0E
R35 green archive/SHA-256:
  tmp/pig-v011-request-aware-r35-factory-green.tar.gz
  E88270937199B2F529CBE669CDC5C1FD1D91B16FD6C43E2940E0C2CBD2A7C18F
R35 combined log SHA-256:
  74108DD86E7FA30DDF759233510C40069AA0E5CFA0804C0547C9C204F56AA80D
```

R33 因旧 factory 仍构造 TTFT reference 而在 shadow/enforce 均失败；R35 证明 production
factory 返回 request-aware adapter，读取 canonical cadence/freshness/TPS/KV 配置，不再消费
旧 TTFT/learner 输入。server package、factory/adapter focused race 与 shadow side-effect-free
race 通过，`gofmt -d` 为空。R34 先被 PowerShell 拆分 test regex，随后又只发现格式差异、
未运行测试，明确不计为 green。

### 13.12 R36/R37 telemetry 与恢复 behavioral red

```text
R36 archive/SHA-256:
  tmp/pig-v011-request-aware-r36-telemetry-red.tar.gz
  8128C383B93D6B71C50541699ED0EACD0A962D54C653D9B042BAF7E80D4619DA
R36 behavioral-red log SHA-256:
  32B2B6943A7AA717B70D4E699F1D4E3292EF01B866A300E5AAF3D88815FC08F9
R36 gofmt diff SHA-256:
  31CA0303C6DE9513E08EEA69845F576100FA3B89D7959C1735E6286CFF8CDFEA
R37 extended red archive/SHA-256:
  tmp/pig-v011-request-aware-r37-telemetry-recovery-red.tar.gz
  514DC949D3F2C70B3FD2DB35C2561BE4E3D34B0BE773180519A51CE4E7900D0E
R37 behavioral-red log SHA-256:
  9F513E035B14A39DCEF75831DDF50005E907CE514233DDFE8CD3540941FC96CB
R37 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R36 的 adapter telemetry 全零、SELECTIVE Router 没有 inspect slot、Prometheus 缺少
request-aware verdict；行为失败有效，但其 gofmt diff 非空，因此不是完整格式门禁 red。
R37 修正格式并新增“无新业务请求仅靠 scrape 从 hard clamp 恢复 OPEN、probe 不改 Manager/
attempt”合同，三组 telemetry 测试继续因 reporter stub 失败，是干净 behavioral red。

### 13.13 R39 telemetry/Router/Prometheus focused/package/race green

```text
archive:
  tmp/pig-v011-request-aware-r39-telemetry-recovery-green.tar.gz
SHA-256:
  04F2343C5BFA5FF7EEF60C7C47924E8BEB3AECE8B56E8A2A8708B7465ECE80B2
focused log SHA-256:
  9B4A3B10A44EB60D27B5C78C5790E4999FCC09968F16D994794CFD35BAF080C9
package/race log SHA-256:
  967CBA1235046BAD97F431308044D094F152EC4C30EE19F975EFD6C1FAEC7B82
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

focused 证明 last request verdict 低基数 telemetry、HARD `inspectCapacity=0`、SELECTIVE
`inspectCapacity=1`、shadow 不 clamp、无业务请求恢复 OPEN、probe 无 reservation/attempt/
event side effect，以及 request-aware Prometheus 指标。server/metrics package 与两组 focused
race 均通过。R38 只用于取得非空 gofmt diff 并据此修正格式，未运行测试，不计为 green。

### 13.14 R41/R42 统一 verdict 日志 red/green

```text
R41 behavioral-red archive/SHA-256:
  tmp/pig-v011-request-aware-r41-decision-log-red.tar.gz
  67F17C0618B1503ECC9497E7001C5A495DB1808487D2FBBBFEA9B8666BCA3A53
R41 behavioral-red log SHA-256:
  90690DFD6A5A583312AE1793F37142CE9045FD9ACD626ACF18795C3D79E8AADD
R41 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
R42 green archive/SHA-256:
  tmp/pig-v011-request-aware-r42-decision-log-green.tar.gz
  D30BCA9A8B3588D6567A60501BA6FD62DFF2991D45D771C682EAC8F373FFE7CF
R42 focused/package/race log SHA-256:
  AC0A982849F11891FC6BD045D63ACF3814855F31686A9BD43BCC528A0663FDC7
R42 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R41 干净地失败于 protection decision 没有写日志；R42 证明日志直接消费同一个 immutable
verdict，包含 action/reason/scope/pressure 和数值字段，不包含 request ID、model、prompt、
body 或凭据。重复相同签名按一秒窗口有界抑制，下一条记录带 suppressed 计数；callback 在
adapter 锁外执行且 panic 被隔离。focused、server package 和 focused adapter race 通过。

### 13.15 R44 真实 proxy HTTP integration green

```text
archive:
  tmp/pig-v011-request-aware-r44-http-integration.tar.gz
SHA-256:
  D30B41102F4586AD2B7B427CB10F0218F29962ED18060B39E1F26129147AD5D0
focused/package log SHA-256:
  B0C13820D961C0E3C1584ED8F2B6ACE281EAE828245559C6AD7BA3B01FC19C06
focused HTTP race log SHA-256:
  DDF3A50BFBB8F8AE28C315264B571707EB4E31A7ED62109E2B33CFA5D186AE10
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

真实 JSON classifier/estimator 和 proxy HTTP 路径证明：同一 pressure snapshot 下 small request
得到 HTTP 200 并 forward，large request 在 upstream 前得到 HTTP 429；HTTP、日志、metrics
与 Router state 来自同一 verdict。shadow 对同一 would-protect request 仍 forward、无 enforce
reservation、无 Router clamp。R43 仅因测试文件 unused import 无法编译，不计 behavioral red。

### 13.16 R46 hard guards 与并发 burst green

```text
archive:
  tmp/pig-v011-request-aware-r46-hard-guards-burst.tar.gz
SHA-256:
  DD4591F4B248DE62C1225192784F25D9DCA0A76EA48BAC1FAB1618678C659675
focused/package/race log SHA-256:
  6289B15C4BFB51368E6A91CFF4B530DED08246DFA11ABB0E7E25C0CA7C8CD68F
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

真实 HTTP 路径覆盖 stale、preemption cooldown、epoch invalid/Manager intake closed：均在
upstream 前 HTTP 429、`hard_protect`、`inspectCapacity=0`，保护日志 reason 对应。Manager 在
1/16/64/256 并发 burst 下证明 check/decision/reservation 同锁、virtual KV 不突破 hard limit、
terminal exact-once 后无 reservation leak；focused burst race 通过。R45 的 hard-guard 部分虽
通过，但 burst 测试引用不存在的 terminal enum 而编译失败，因此不计完整证据。

### 13.17 R47/R48 旧 learning 配置解耦 red/green

```text
R47 behavioral-red archive/SHA-256:
  tmp/pig-v011-request-aware-r47-config-decouple-red.tar.gz
  98360938FB46D19AC7A4334E7A0D09CE8A5C2C299C7B78B853B0F0FCE8187A5A
R47 behavioral-red log SHA-256:
  A3607E36EBA7F40D8AABC853F1557465C75E76225EB18A962FDCADBB08244A02
R47 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
R48 green archive/SHA-256:
  tmp/pig-v011-request-aware-r48-config-decouple-green.tar.gz
  8491183D0F0B2F97555E15556C384578D6D9C71D2EE0F6FE874A928455DF3BA9
R48 focused/package log SHA-256:
  B678E55816044A1F9341FD25AEB33C91D0484F19EF8F1CCB86F820B3DDD1621A
R48 pigconfig/server race log SHA-256:
  A010D92EA9A7F23CB08652A2E32BC3A73BADCCB3384F28152577D56C753F43A2
R48 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R47 分别证明旧 learning env 仍可阻止 Load、旧 learning 字段仍可阻止 Validate，以及
`maximumAge < pollInterval` 未被拒绝。R48 使 v0.11 loader/validator 不再消费这些 legacy
learning/calibrator multiplier；同时强制 poll/maximum-age 正数、不超过一分钟，且
`maximumAge >= pollInterval`，避免每个采样周期之间稳定进入 stale 误锁。Config struct 和
production metrics 中的旧字段仍待 dead-code 审计后删除；本节只证明 Load/Validate 解耦，
不是旧 learning 源码已完全清除。

### 13.18 R49 dead legacy factory cleanup green

```text
archive:
  tmp/pig-v011-request-aware-r49-legacy-factory-cleanup.tar.gz
SHA-256:
  E34FDF21E03E038CB5A21F64C6C039BA8F286C741D1FB2329A333EEA3871890A
factory/server/race log SHA-256:
  5B93249C825DDFFE1A37C4C37716DFE963A031FFF678C17BD48752B52B3BA17B
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

删除无 production 调用的 `newLegacyPredictiveShadow`，以及只为它服务的 model-profile、
learner、TTFT/TPOT、prefill penalty helper；保留新 factory 使用的模型无关 manifest ID 和
block-aligned hard-KV helper。同一 exact archive 上，新 deterministic factory 构造、protected
KV、完整 server package 与 focused factory race 通过。本节仍不证明 legacy metrics/config/
observer/source 已完全删除。

### 13.19 R50 retired production metrics behavioral red

```text
archive:
  tmp/pig-v011-request-aware-r50-retired-metrics-red.tar.gz
SHA-256:
  EF98FD1A93EF48E540270E166F4CD48FD35BA707E04298859C892412230587D1
behavioral-red log SHA-256:
  0705E9380622A789149015F4F4D63C9C1A995979E780C6F039F88B874D4E1281
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

新增 production metrics 合同：保留 request-aware verdict/pressure 与当前 Router capacity，
不再输出 retired learner、input calibrator、existing-prefill、deferred outcome 或 pressure-bucket
指标。测试干净失败于输出仍包含 `pig_predictive_learning_`，证明 red 是预期行为差异，不是
编译、依赖或格式失败。

### 13.20 R51/R52 retired metrics cleanup partial/green

```text
R51 archive/SHA-256:
  tmp/pig-v011-request-aware-r51-retired-metrics-cleanup.tar.gz
  53C50EB4A5679C0770ACAABC290F27C5E68CF60C22E223161EB8B215BF62F889
R51 log SHA-256:
  B3762ADDE995C6B24429E5F2CF4F2183FDDC78CCFB0A8B18961EA3F4E06AFB13
R51 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
R52 green archive/SHA-256:
  tmp/pig-v011-request-aware-r52-retired-metrics-green.tar.gz
  45E2936F464D5A8CD94F97BCB4A53259D28DB41D336FD41F88F4486D097FE7B4
R52 focused/package/race log SHA-256:
  E2CC808E984AB47BCA512E7DCAECEBFFD2C69285FC0B1ECB865A7A2C4DCA68D8
R52 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R51 的新 retired-metrics focused test、metrics package 与 runtime predictive package 通过，
但 server package 中两条旧 approximate-path 测试仍要求已删除的 learner/renewal 指标，因此
R51 不计 green。R52 更新这些冲突断言后，证明 production 输出保留 request-aware verdict、
bounded labels、Router inspect capacity、failure/hot-path histogram，同时不再输出 retired
learner/calibrator/TTFT/prefill-learning 指标；metrics、runtime predictive、server packages 与
metrics/factory focused race 通过。

### 13.21 R53-R55 config/metrics SOLID cleanup

```text
R53 archive/SHA-256:
  tmp/pig-v011-request-aware-r53-config-metrics-solid-cleanup.tar.gz
  FB21C4A5FEACAF6ADA1B2B52F26E9BDD1C528C029BFE3C3F44B93484D70CA5F4
R53 log SHA-256:
  42FEBD7CECEC52B638FA064D0B932526B7F46801EA89B5ACD58188CFEABDB510
R53 non-empty gofmt diff SHA-256:
  70077DA0C526B3D93D70358E195B0E27641D2141D28F4ED53E550B7D486B712B
R54 archive/SHA-256:
  tmp/pig-v011-request-aware-r54-config-metrics-solid-green.tar.gz
  453AB6E33465930467C3F8D4D9C296F0EE0D73CAEA9203F46FF424DCCD82C064
R54 log SHA-256:
  73ED8FFD892B3DC374DF71600840E19EAB5FE65EB70A3F18CAE07E66D204395B
R54 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
R55 green archive/SHA-256:
  tmp/pig-v011-request-aware-r55-config-metrics-solid-green.tar.gz
  E745EBF69A457EE081DB43F5C96F3A14EBE9A772A8D45497F2F63D9257BA842D
R55 package/focused-race log SHA-256:
  29775CE895731559F83BC745EAEE76304F02B6CBDA18112A51FC2261DA98281E
R55 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

清理删除 loader/validator/factory 均不再消费的 legacy learning/calibrator Config fields，并从
每次 metrics scrape 和 status log 中移除对应 dead snapshot copying；状态行改为直接显示
request-aware action/reason/source、pressure、size/KV/load 与 Router inspect。R53 只发现格式
差异、未运行测试；R54 的 pigconfig/metrics/runtime packages 通过，但 server test build 被两条
显式 v0.10.13 shadow-prefill metrics 字段引用阻止，因此两者都不计 green。R55 删除仅验证已
退役 metrics 映射的历史测试段后，pigconfig、metrics、runtime predictive、server packages
以及 metrics/server focused race 通过。R55 的 config race 正则没有匹配测试，明确不计 config
race；完整 race 仍待后续同一 exact archive 门禁。

### 13.22 R57-R59 deterministic simulation red 与算法修正

```text
R57 behavioral-red archive/SHA-256:
  tmp/pig-v011-request-aware-r57-simulation-red.tar.gz
  D9AA211D8BD602E39EA2551E8543CC91BBFA854D4E4898F8D0229928E6D99B5A
R57 log SHA-256:
  F4554FB71C0397B8C26CE5D0EE66B625EB040F47A8414D986769DD70508F8F8B
R57 gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R58 first implementation archive/SHA-256:
  tmp/pig-v011-request-aware-r58-simulation-first-green.tar.gz
  BBEF087D5769992E62504F5B1B38D4F0F673CFB2676986C6FA986401122DE622
R58 simulation log SHA-256:
  68FAAE8B430269C14F633DCA8DC06469569DFE3363BA6498D15BB3F6E29C4436
R58 non-empty gofmt diff SHA-256:
  C42F41651677487ECF4BC8B3ADF6A4A4EDAB9C4647E9BB612BCBCCE327F2AD9F

R59 diagnostic archive/SHA-256:
  tmp/pig-v011-request-aware-r59-simulation-diagnostics.tar.gz
  48157F6BC8ED77E2F82CBD7C37A3975F09EF3CC7B61BDF52C3BB1D7814C308A5
R59 diagnostic log SHA-256:
  D4C492C6550EB68D6D4995C674455BD7E96F687C3DB95829DDA2CDC6A846CB5E
R59 non-empty gofmt diff SHA-256:
  71346BEF7400CAB9512F290D9DAA4C9D4022418D77E5B02CBB15385F458A25CF
```

R57 干净失败于 simulation 没有调用 production `RequestAwarePolicy`，是预期 behavioral red。
R58/R59 已证明 23 个预注册场景能编译、固定 seed 可重放、且实际调用 production policy，
但不能计 green：R58/R59 均有非空格式 diff，acceptance 首先失败于 `short-only` candidate
TPS-floor violation `0.4s > 0s`。R59 的逐场景诊断进一步发现，脱离 engine ground truth 的
`tps-target=24` override 会持续隐藏实际降速，使 candidate 出现 `6.3s` floor violation 和
`6.1s` waiting；这是 simulation 因果错误，必须删除，不能用调宽门槛掩盖。

R59 同时暴露 production 算法的真实量纲问题：以大额 `remainingKV` 乘 `(1-pressure)`，
waiting/TPS pressure 对普通 request size 几乎没有作用；full pressure 固定保留一个 block 又
允许 tiny request 持续穿透。下一 red/green 必须同时完成：真实 generation-delta TPS 场景、
post-admit TPS forecast、弹性 KV band size allowance、full-pressure zero allowance，并重新跑
production policy/Manager/server focused tests和全部 simulation acceptance。在这些证据完成前，
simulation 仍为 red，v0.11.0 仍不可部署。

### 13.23 R60/R61 post-admit TPS forecast behavioral red

```text
R61 clean-red archive:
  tmp/pig-v011-request-aware-r61-post-admit-forecast-clean-red.tar.gz
SHA-256:
  09EB374CF678592B97FF19DBD79D298724D98540721A73E99378C3B0F11A175D
runtime behavioral-red log SHA-256:
  259C2A96027BEDF8A48D5B7207400F3C4D84F635CEE5D6CF8BB511719783FF6D
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R61 干净证明当时 production policy 仍未消费 post-admit TPS、同一 snapshot 的
`effectiveSequences` 不改变 verdict、full pressure 仍允许 tiny request、size allowance 仍按
全部 remaining KV 缩放，且 forecast telemetry 为空。R60 是 runner/preliminary 尝试，R62 是
partial implementation；二者均不晋级为当前 green 证据。

### 13.24 R63-R65 soft-KV double-charge false-green/red/green

```text
R63 first forecast archive SHA-256:
  48C26AE7960A0EA71F4D5FF5BF71C9AF4AD255D412E4491E5C21AD0D34B860E5
R63 runtime/observer/simulation log SHA-256:
  F1B4ED34D8D44E15F4164824B50781C42C643F1522B4DBC63524A3FFDC1FC191
  19354B3313A2E2682DE4E9EC4DF52F5FECF37841A6CFFCA1315381F8899732DE
  9F3AA64F54F67B7C9287597D6E3C694E323F101AC9EA37B6BC0B3FD5CEAF3170

R64 clean-red archive:
  tmp/pig-v011-request-aware-r64-soft-kv-double-charge-red.tar.gz
SHA-256:
  41623A234EE9B53076081CB0993BA6714F375E14F5560E1400468DC89B243DDB
runtime/simulation behavioral-red log SHA-256:
  0F20C048BFA7D77EAFCB2009193154164D66E61F225342427E5E3994AEF63164
  9BEEA8781BEB819B8E13ECC4FFEB66AAB8B329642109BB5E91AC6036D84E8B92

R65 focused-green archive:
  tmp/pig-v011-request-aware-r65-soft-kv-double-charge-green.tar.gz
SHA-256:
  597ECF0C8F8C62420CF78631D5FE64F1ACBA4FC5AA816CAD9D854306FDA9E05A
runtime/observer/simulation log SHA-256:
  DF0C37B4F6A7709B91DB6BF596D208F0EB21511016461A3426485AD7A8DF57AD
  7436ABF2BB00A038FD5FE990820DE0FD25C0B2C5B69D73409E7E6A8FF6036EE4
  80EB8087BFA69BF86A76A2169DCAAEFD403FA056238E524C42940274E7243382
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R63 虽通过当时 acceptance，但 `large-only` goodput 从 `91.898` 降至 `72.333`，
`large-small-output` 从 `75.333` 降至 `60.000`，且 QoS 无改善；它是暴露验收缺口的
false green，不能作为晋级证据。R64 用新增回归 gate 干净复现该失败；R65 将完整 request
reservation 只用于 post-admit hard-fit，soft KV pressure 改回 current effective KV 后，两个
场景恢复到 baseline，mixed/order 改善、preemption/waiting/低流 gate 继续通过。

### 13.25 R66/R67 forecast telemetry red/green

```text
R66 clean-red archive SHA-256:
  0CC0A409A93D35B27DD92DD5BDD6FA9C82CABE9749B30394C76A651CA602D72E
metrics/server behavioral-red log SHA-256:
  40F19F650C6E0C4F8A20FB66BFD725C5728BD8F126FF44E49CB35C3740334501
  9B3C68E093B8F9E5197E82E47B5EA65E3F56957FA8995AC6EC880A8A16564C01

R67 focused-green archive:
  tmp/pig-v011-request-aware-r67-forecast-telemetry-green.tar.gz
SHA-256:
  809356344F61B78EC0FBED30F1FDC1DBC63C365D29D02C6E88A532CB095D10C2
metrics/runtime/server/simulation log SHA-256:
  D8A502E6F41450FA87AA5A5D643B4C89F163DB6994CD7225D954D3B7ECC63523
  CEA0FF9EE2576182783A4307BCF2D983DE2A24F03946DAECDD0EE919DDC2BAAE
  1B3861EE06AFEA0AFDBB9B63A7F2BC9EBFEA43DE5E49BF76DC532FAA18C87418
  80EB8087BFA69BF86A76A2169DCAAEFD403FA056238E524C42940274E7243382
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R66 证明 post-admit KV、effective sequences、aggregate/current/projected TPS、forecast validity
尚未接到真实 metrics/status/log；R67 使这些输出统一来自同一个 immutable verdict/input，并在
同一 exact archive 上通过 metrics、runtime、完整 server 和 simulation focused packages。

### 13.26 R68 deterministic JSON report

```text
archive:
  tmp/pig-v011-request-aware-r68-simulation-json-report.tar.gz
SHA-256:
  A1DA93936948C0A1B36E67D45D5CEA778E0D4D4229AD6D4AF3736E5F0A3E037F
command-test log SHA-256:
  72467488D921C7C6B324731492E797431A3F44EC85474994BE8DC651F62804B0
report-1/report-2 SHA-256:
  2DD144B52F8037D5EB39A7BCF9419486EC7ED021F41E263A0F82349842EAC96A
  2DD144B52F8037D5EB39A7BCF9419486EC7ED021F41E263A0F82349842EAC96A
```

两次 JSON report 字节级相同。固定 seed 合成 aggregate 为：baseline/candidate completion
tokens/s `80.810/98.626`，SLO completion tokens/s `80.724/98.492`，preemptions `1/1`，
TPS-floor violation `0.2s/0.3s`，waiting `5.0s/5.0s`，demand 下最大 idle `0/0`。这只能说明
当前合成模型中通过当时 gate 并显示约 22% goodput 改善；不能外推真实 GPU TPS、utilization
或生产吞吐。R68 的外围 status 输出受 PowerShell/SSH quoting 破坏，不计证据；JSON replay 和
command test 本身有效。

### 13.27 R69/R70 suite aggregate TPS-floor budget red/green

```text
R69 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r69-aggregate-tps-budget-red.tar.gz
SHA-256:
  478DF8F3B7EF589A054D467AC2587966D2F613D0997009039FB0B0C99235B4C0
behavioral-red log SHA-256:
  D640426EDAC77BE6F2454059E17DE94433B2CB84482CECFEE7915B4F8EFD2BAF

R70 focused-green archive:
  tmp/pig-v011-request-aware-r70-aggregate-tps-budget-green.tar.gz
SHA-256:
  41B786EE8A7A31A321432DFD2C57E6B66A1B37CED2EE630CDD0F58126564A2B2
simulation package log SHA-256:
  80EB8087BFA69BF86A76A2169DCAAEFD403FA056238E524C42940274E7243382
command package log SHA-256:
  3669F176F33B8B53B56BE95F2F42F4257FF41F527CF63C2E5EF62518FD94B8C3
report-1/report-2 SHA-256:
  2DD144B52F8037D5EB39A7BCF9419486EC7ED021F41E263A0F82349842EAC96A
  2DD144B52F8037D5EB39A7BCF9419486EC7ED021F41E263A0F82349842EAC96A
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R69 的两个独立 scenario 各消耗 `0.1s` 逐场景容差，旧 acceptance 错误地允许 suite 总共
退化 `0.2s`。R70 在逐场景检查后增加 suite aggregate gate；真实 suite 的
`0.2s/0.3s` 仍在唯一一个 100-ms tick 预算内，合成累计用例被正确拒绝，两次报告继续字节级
相同。R70 只证明当时的 focused simulation acceptance；当时仍待验证的 healthy-burst
forecast、block-aligned limits、waiting aggregate 与容差量纲由后续 R71-R74 取代。

### 13.28 R71-R74 第四轮计划审计 red/partial/green

```text
R71 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r71-plan-audit-clean-red.tar.gz
SHA-256:
  12B54485DFBA27AA98F85FC30F05F56021A42D4CE7302B041B66701E9CEA9ABC
runtime/simulation behavioral-red log SHA-256:
  A75544B267D6174E4D64930A761254245BD155D3628B1D7EED5548FB99D06E0A
  B2FA8D13B71E65FC0232C5FFD42F3E43831BD66AEC0B337A4FF477EA848A6512
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R72 partial archive SHA-256:
  E0325B1492095A36DDEDA8A0684DE2BCAFF2724A99D91039FD8FA327AC718C43
R73 focused partial archive SHA-256:
  F8A4D47C4E46291959D94C4EE7DCCCC7A314B9ED16FF55B5A7D5053E4F4CAFC7

R74 focused-green archive:
  tmp/pig-v011-request-aware-r74-plan-audit-focused-green.tar.gz
SHA-256:
  72F509F552582018803B358514E9FD2BE34C124ABC3641CD3513DB2170BF06A8
config/metrics/runtime/server/simulation/command log SHA-256:
  D0EC12D69294923839E65FA4F2ACAC0D58F64C6130AD2E199451E7B89A24FB98
  D8A502E6F41450FA87AA5A5D643B4C89F163DB6994CD7225D954D3B7ECC63523
  FD8EA99C28DB0D7BD0A57043803203C261E09C739098722C1DC2BF91011D6C18
  37FBAEE766FCCD041F8F3E5B94BF3ACB77FE684A4A6D3AA92FEDA7F5FEADD007
  80EB8087BFA69BF86A76A2169DCAAEFD403FA056238E524C42940274E7243382
  3669F176F33B8B53B56BE95F2F42F4257FF41F527CF63C2E5EF62518FD94B8C3
focused race log SHA-256:
  AE679C8F2724D11B940079051FF25F2148276DA7A0758C43758B0F66B80231A4
report-1/report-2 SHA-256:
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R71 干净证明：(1) 健康 TPS 分支会让同一 poll 内第二个 reservation 继续复用旧均值；
(2) `BlockSize` 未参与 operational KV limits；(3) waiting duration budget 可按场景累计；
(4) seconds tolerance 会掩盖 goodput 退化；(5) idle/self-lock gate 错误允许一轮 poll 再加一轮
simulation tick。R72 修复 production policy 后，两个只应验证 KV atomicity 的旧测试仍携带
有效 TPS 样本，因新 TPS 保护只 admit 一个而失败；该 archive 不计 green。R73 将两个测试的
TPS 明确设为 unknown 后，runtime/simulation/command 与 replay 通过，但 server 的 telemetry
断言仍使用未 block-align 的 `remaining=4000/allowance=599`，因此仍只计 partial。

R74 在同一 exact archive 上通过 config、metrics、runtime、完整 server、simulation、command
packages 与 runtime/simulation focused race；两次报告字节级相同。新合成 aggregate 为：
baseline/candidate completion tokens/s `80.810/98.485`，SLO completion tokens/s
`80.724/98.398`，preemptions `1/1`，TPS-floor violation `0.2s/0.2s`，waiting `5.0s/5.0s`，
demand 下最大 idle `0/0`。这仍只是 deterministic/focused builder 证据；full repo、vet、完整
race、benchmark、三轮 release review、commit/push/image/deploy/live 均未完成。

### 13.29 R75/R76 Close 与 forward commit 线性化 red/green

```text
R75 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r75-close-forward-commit-red.tar.gz
SHA-256:
  5F57775F7643C67668B96EDB27D1C5AA4F101C26E05A4FA2B00999C368216A2E
server behavioral-red log SHA-256:
  8DB30197D53B63996C5BEDF84AB3C3272DAF6EBA2BF608E5BB4520FC246681B6
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R76 focused-green archive:
  tmp/pig-v011-request-aware-r76-close-forward-commit-green.tar.gz
SHA-256:
  92C4C9872AD25A9BE493ACB060DD88AA801B9C6E21AE7CBC1AB20EF0D94F86B2
server/server-race log SHA-256:
  616A65737EE78DC6A7617E31B7210D7E92E6A44AB6D7B08EF797F94F6A0C8DA5
  C4A356641DFA7051C2A53DC8D197480C9E3E2F4F6BE3E08858859599DC8AACB5
simulation/command log SHA-256:
  6C0AE0BC18295661B63C8D4D89F6D8086915BDC2A4AD704BF5AD6CB49ECF48F0
report-1/report-2 SHA-256:
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R75 干净证明旧 reservation 在 adapter `Close()` 已先线性化后仍可
`MarkForwarded()`。R76 让 request-aware reservation 持有 adapter owner，并用同一 owner 锁
线性化 close 与 forward commit：close 先发生则 commit 返回 false，由 HTTP deferred terminal
释放；commit 先发生则该已提交请求正常完成 lifecycle。修复没有增加第二套 reservation registry。

### 13.30 R77-R79 enforce 旧 QoS/tier/priority 热路径审计

```text
R77 invalid partial archive SHA-256:
  CD585A0B27ED5FD276BFB120DF90DDADF68B580431CE94D1E16E7AA1BE520285

R78 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r78-retired-qos-hotpath-clean-red.tar.gz
SHA-256:
  535A145D084689BC15CFA8B6F9AF49B1A94C52E392DFB55A5FF39155A9A5537E
server behavioral-red log SHA-256:
  2B62223E7ABC847829603A39C09CAFF1C728F96635CCB364299560639E455699
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R79 focused-green exact-source archive:
  tmp/pig-v011-request-aware-r79-retired-qos-hotpath-green.tar.gz
SHA-256:
  1B464096F8239ED871A9E053800CAD08016B19389B139599959B51D940679174
config/server/server-race log SHA-256:
  D0EC12D69294923839E65FA4F2ACAC0D58F64C6130AD2E199451E7B89A24FB98
  8FB7E3A727EFE05CA700800B1625497A29120B7C5C305072F79AE60F949FF3C8
  21B52EBDA386AF157887EE223D4E098DBBBBF35B33C77F151416F22B4187620A
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R77 的 static-gate 失败有效，但 priority fixture 缺少独立 stream-buffer 配置，因此不计完整
behavioral red。R78 干净证明 policy-fit 请求仍会被 `GlobalLimit=0` 的旧 gate 429，且
`priority:100` 仍被 rewrite 为 `-100`。R79 的 enforce 路径跳过旧 `qosGate.WaitAcquire`、tier
accounting 和 tier/lane header，并把 backend priority injection 的 effective config 关闭；shadow
继续保持旧服务行为。R79 仅通过 config、完整 server 和 server race；该 exact source 没有重跑
runtime/simulation/report，更不能继承 R76 的 simulation 证据。

### 13.31 第五轮 Router contract 审计（待 red/green）

只读对照既有 Router consumer 后确认，Router 在 metrics 新鲜时用
`observed_waiting>0 || observed_running/global_limit>=1` 判定 blocked；非正 global limit 的
fullness 为零且 route 保持可用。因此当前 enforce 虽绕过旧 PIG QoS gate，仍会被 raw dynamic
waiting/global limit 在 Router 二次限流，尤其会把 waiting 触发的 SELECTIVE 退化为节点整体
blocked。下一轮必须以可编译 behavioral red 证明以下合同后再修复：

- off/shadow 的 existing metrics 字节语义保持不变；
- enforce OPEN 发布 raw waiting/limit 供观测，但 effective waiting 为零、effective global limit
  为 neutral non-positive sentinel；
- enforce SELECTIVE 发布 effective waiting `0`、global limit `effectiveRunning+1`；
- enforce HARD 发布 effective waiting `0`、global limit `effectiveRunning`；
- raw dynamic waiting/limit 改变不改变相同 request-aware projection；
- 没有新业务请求时，fresh OPEN scrape 能立即解除 SELECTIVE/HARD clamp，不产生 sticky lock。

在上述 red/green 与新的 exact-source simulation/full gates 完成前，v0.11 仍不可部署。

### 13.32 R80-R82 Router 唯一 effective authority red/green

```text
R80 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r80-router-authority-red.tar.gz
SHA-256:
  A973BBC108E4892801750891B2B17B648D9C642DE2AB776F10F4CF6D894BFAE8
server behavioral-red log SHA-256:
  FB3E945ECB99004B952675B47B50C0A04D9242AAF1ACFE8C3E2480C5B4C4CEEF
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R81 new-contract focused-green candidate archive:
  tmp/pig-v011-request-aware-r81-router-authority-green-candidate.tar.gz
SHA-256:
  1B56A99BF1B6AC783E8CE877D3591CFD2E2EC6D0D3BEAF0D020107E55D6214C9
focused log SHA-256:
  5CB49D1FCFDC7AF31CC9C1F33148F32B3BFB97DFEC1D391567DD9BBF3177690F
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R82 server-green candidate archive:
  tmp/pig-v011-request-aware-r82-router-authority-server-candidate.tar.gz
SHA-256:
  FCC9B05221B1D0B916A68ABDDD946A855D560FCCE6FCD533C01874C0F9EAFB83
server log SHA-256:
  BF18CB0E762588065C3C72A1B500A1978BACE9C9C7BD102740537301A4A7299C
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R80 可编译 behavioral red 同时证明：(1) enforce OPEN 仍继承旧 limit `1`；(2) SELECTIVE
期望 `running=3/limit=4`，却被旧 limit 截成 `1`；(3) HARD 同样错误为 `1`；(4) raw waiting
没有单独指标，Router-consumed waiting 仍为 `7`。R81 用 raw/effective waiting 分离和
request-aware-only effective limit 修复新合同，新增 focused tests 通过；R82 同步修正旧
“enforce 恢复到 dynamic limit=50”的历史断言后完整 server package 通过。当前实现语义为：
off/shadow 保持 raw dynamic projection；enforce OPEN 为 `waiting=0/limit=0`；SELECTIVE 为
`waiting=0/limit=effectiveRunning+1`；HARD 为
`waiting=0/limit=effectiveRunning`。R82 尚未通过 metrics package、race、simulation 或 full
matrix，仍只计 server-focused source evidence。

### 13.33 R83/R84 enforce priority-only validation 解耦 red/green

```text
R83 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r83-priority-validation-red.tar.gz
SHA-256:
  9D7D6FFEAD5E91A9F1F9F0918A28A21696E6A0D5AC412DC4DD32D65F2626E203
config behavioral-red log SHA-256:
  34B6BE2859A595BE2616BE32358A46F007DB278ADD760132AB12A0A31A2D176A
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R84 focused-green exact-source archive:
  tmp/pig-v011-request-aware-r84-priority-validation-green.tar.gz
SHA-256:
  BD53E3C58A4564857150B50794636EAC6D6A206B41A1415961DEC4A49F0FBB66
config/metrics/server log SHA-256:
  0FE8EE6BF4121E83E90D18B2CA970E06E098BF32D8B3F9ECEF41E255E38B0135
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R83 干净证明 request-aware enforce 会因已禁用的 `BACKEND_PRIORITY_MODE` 失败；同一个 invalid
fixture 在 shadow 中仍应失败。R84 让 `validatePriorityConfig` 仅在 effective backend priority
可能启用的 off/shadow 模式执行，config、metrics、完整 server packages 通过；OpenAI
compatibility 的独立 loader/validator/rewrite 没有修改。R84 尚未跑 race/simulation/full matrix。

### 13.34 R85 Router advisory race 接受边界

```text
R85 exact-source archive:
  tmp/pig-v011-request-aware-r85-router-advisory-race.tar.gz
SHA-256:
  0BFF193D3B6628FA9DBD0739C324C15B9BFB80186339BD7889944F0819DAD607
focused count=10 log SHA-256:
  05A83655EF2B84B21B71374C241206DDA8F7BC658102716BF25522A0B41FD3DB
focused race log SHA-256:
  AAB12A57AF4C9EDE39CC0746A14F2B059580CDA682B8ADD529DE09DCF8B7EBBF
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R85 新增 concurrent telemetry scrape/admit/forward/terminate test：128 个业务 decision 与
8 个 scrape worker（每个 256 次）并发，focused 连跑 10 次及 focused race 均通过；最终
reservation 为零、virtual KV/sequence 回到基线、business attempts 精确为 128，fresh scrape
撤销 Router clamp。该证据支持 3.28 的 bounded advisory-stale 接受边界，但不把 metrics scrape
与下一次 Router 网络请求宣称为原子事务，也不替代完整 race/full matrix。

### 13.35 R86 exact-source benchmark 与 deterministic replay

```text
R86 exact-source archive:
  tmp/pig-v011-request-aware-r86-benchmark-candidate.tar.gz
SHA-256:
  BC87810056BBA25CA526AB78D058F5FE26AB2C228FF25F4C34BCDDC78DEE0A54
policy benchmark log SHA-256:
  3DBF9FE135CA7A4000A66EE489ED4CFE980FF844F0F81589AEEE8203FD41E917
estimator benchmark log SHA-256:
  91350A84BD7CBFBEC221CCF5B21598C417CD9987443F9BEEFB2A3EDA73B3661A
HTTP benchmark log SHA-256:
  F121E29173D26B8744E1F12CD3D4F356C9664679300D6DC4EE9F0658D6306713
simulation test log SHA-256:
  6038835DFD30AFF0B25C9F2D709A7D613E12D6AC8A9DE531CB1B345A7A6E5015
report-1/report-2 SHA-256:
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R86 builder 数据：pure policy open/selective/hard 分别约
`29/31/27 ns/op`，全部 `0 B/op, 0 allocs/op`；完整 estimator 1 KiB/64 KiB/2 MiB 约
`0.271/1.964/64.3 us`，全部 0 alloc；lexical JSON hint 本身约 `0.09 us`、0 alloc，但它只是
常数级提示而非完整 body estimator；3.6 KiB pre-forward protection HTTP benchmark 约
`46.8 us/op, 39.3 KiB/op, 119 allocs/op`，包含 `httptest` request/recorder 构造，不能当作生产
incremental latency。deterministic simulation packages 通过，双次 JSON byte-identical，aggregate
仍与 R74 一致。R86 后又发现 coarse status 问题，因此不能作为最终 release exact source。

### 13.36 R87/R88 predictive coarse upstream status red/green

```text
R87 clean behavioral-red archive:
  tmp/pig-v011-request-aware-r87-upstream-status-red.tar.gz
SHA-256:
  44EA52B493833F6F7B650F34B3FD1FBEE85BAFDA500F3BEA44DEE5A329620DD5
server behavioral-red log SHA-256:
  447A05989FED3850EB8CC2CCA8E7BDF11A609DAC929A593789BAD410F4A625F4
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855

R88 focused-green exact-source archive:
  tmp/pig-v011-request-aware-r88-upstream-status-green.tar.gz
SHA-256:
  D0D292A112EE726F7F07E6A145E1F5E7C8BBA07A1B5E490C8DEF248B814966FD
server/focused-race log SHA-256:
  EE7ACF9D9E20EE1D8D6D0B734C00FB3C72AFC7C632B71BC38173616998779FB4
  4F4E9818204E8049BBE2FBB6E8F69539D10788B49454CF75ECB2BD67E56D1DEB
gofmt diff:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R87 clean red 证明 enforce OPEN/SELECTIVE/HARD/availability 全部因 legacy QoS state 缺失而返回
unknown `3`，且 provider 缺失没有 fail closed。R88 让 enforce status 直接读取当前
`PredictiveAdmissionTelemetry()` inspect：OPEN/SELECTIVE/HARD 分别 green/yellow/red，availability
和 provider 缺失为 red；shadow 继续使用 legacy status。完整 server 与 focused race 通过，但
R88 尚未重跑 benchmark/simulation/full matrix。

### 13.37 R89/R90 第五轮计划复查 behavioral red

```text
R89 exact-source red archive:
  tmp/pig-v011-request-aware-r89-plan-review-red.tar.gz
SHA-256:
  17C0281A379AB713C2223E81294A159001FC798010CFCF5E8AEE9E8204C64977
valid behavioral-red log SHA-256:
  unknown TPS policy:  1F8852DDD677F5DCF10376017547B4F7588E2449604685687083C248DC146854
  unknown TPS manager: CC22BE95DFF572297707E323DC75F0FAF688DB3DA1703492A47F7B0BC45CFaa9
  startup baseline:    40CDBDAE928B335F1A3FB96B512797680250B4E99B30D56029219EF2DB4DAC91
  retired hold config: 3F25C37CF0861EA98FDE4959BA12E39CA2E0F5448F8A4ABD8BA0999F978411EB

R90 corrected counter-reset red archive:
  tmp/pig-v011-request-aware-r90-counter-reset-red.tar.gz
SHA-256:
  627E6AEDA370C9E0A786876080E2213D77E5952EB8C93DDA1AA19C3AD7AB481C
counter-reset behavioral-red log SHA-256:
  99DB7416B97904A4ACBD7854C9D83F213E22045D43E0D6CDE6E9C02BB40A6557
```

R89 的 policy/Manager red 证明 TPS unknown 且已有一个本地未吸收 sequence 时，旧实现仍以
`pressure=0` 或仅 KV soft pressure 继续 reserve；startup red 证明 probe counter 已复制但
`requestAwareHasBaseline=false`；config red 证明已不参与 request-aware policy 的旧 hold env 仍可
阻止 Load。R89 的第一版 counter-reset fixture 在 reset 前因 `RequestCost` 内部维度不一致失败，
不计 red 证据。R90 修正 fixture 后，旧实现成功创建 old-epoch reservation，再在同 identity 的
generation reset 后返回全零 stale input，证明永久 quarantine 行为差异。所有有效 red 均可编译，
没有缺依赖或 runner 失败。R89/R90 都不是 green 或 release evidence。

### 13.38 R91 formatting reject / R92 focused green

```text
R91 candidate archive:
  tmp/pig-v011-request-aware-r91-plan-review-green.tar.gz
SHA-256:
  C0678290D2261B6039121D4649D93DC633DE73B0737B8D41DF6BED2DF9C158A0
result:
  rejected before Go tests because whole-repository gofmt diff was non-empty

R92 exact-source focused-green archive:
  tmp/pig-v011-request-aware-r92-plan-review-green.tar.gz
SHA-256:
  45E539209CBFD2B2659CB467335255C3ADE25162241733E20B855A9550B2D1CF
gofmt log SHA-256:
  E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
runtime focused log SHA-256:
  D956A512833BA31B62B92FAB2FFED95CB06DD33D094CD51984A72D78062B8794
server focused log SHA-256:
  5CB49D1FCFDc7AF31CC9C1F33148F32B3BFB97DFEC1D391567DD9BBF3177690F
config focused log SHA-256:
  2BE1D7520B78F7E183D85D1967CB25568A597913300B2427E6B36B2537E6C28F
config/metrics/runtime/server package log SHA-256:
  04DA3938631D28E95396A545CB081AD1594E1BD82D0A2D423687EAB033CEE137
focused race log SHA-256:
  37BDE9CFA6AEA7C771BD2FBBB1F90E094D327C507EF8186C20F08EAB01550DA3
```

R91 只证明 formatter gate 正确拦截，不计测试或 green。按 builder diff 机械修正后，R92 证明：
(1) TPS unknown 的首个 speculation 可进入，同 snapshot 后续 reservation 以 TPS source/full
pressure 保护；(2) 1/16/64/256 并发 unknown-TPS burst 只产生一个 reservation 且 terminal 后无
leak；(3) startup probe 成为第一份 TPS baseline；(4) 同 identity/capacity/block counter reset
原子清空旧 reservation/retired state、拒绝旧 handle forward，并以 fresh unknown-TPS baseline
reopen；(5) retired Router hold env 不再影响 Load。四个相关 package 和 focused race 通过。
R92 之后 README/计划文档又有非可执行修改，因此仍不是最终 exact-source archive，也不替代
simulation、benchmark 或完整 full/vet/race/build。

### 13.39 R93 deterministic simulation 暴露 burst overprotection

```text
R93 exact-source archive:
  tmp/pig-v011-request-aware-r93-simulation.tar.gz
SHA-256:
  CDE3E0CDAE30755203A3E7990B45D94ACC9CFC30DA4339D0AD90A17C4775B0CB
simulation test log SHA-256:
  6C0AE0BC18295661B63C8D4D89F6D8086915BDC2A4AD704BF5AD6CB49ECF48F0
report-1/report-2 SHA-256:
  72721EF4FFCFEB8DC87FDB17E1E69DB12ED82E35D579701D85744C57ECDD8AFD
  72721EF4FFCFEB8DC87FDB17E1E69DB12ED82E35D579701D85744C57ECDD8AFD
gofmt/stderr:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R93 双次 replay byte-identical。suite aggregate candidate 的 completion/SLO tokens/s 为
`97.103/97.053`，baseline 为 `80.810/80.724`；preemption 同为 `1`，TPS-floor violation 从
`0.2 s` 降到 `0.1 s`，waiting 同为 `5.0 s`。但 `pre-poll-burst` candidate 只 admit `1/5`，
completion/SLO tokens/s 为 `70.630/70.630`，baseline admit `5/5` 且为
`102.405/101.571`；candidate 仅把该场景 TPS-floor violation 从允许预算内的 `0.1 s` 降到
`0`。因此 aggregate pass 是 acceptance coverage gap，不能晋级。active contract 已把 burst
加入 1% goodput non-regression gate；下一步先取得该 gate 的 behavioral red，再回退
unknown-TPS single-speculation。R93 不是合格 simulation 或 release evidence。

### 13.40 R94 burst acceptance behavioral red

```text
R94 exact-source red archive:
  tmp/pig-v011-request-aware-r94-burst-acceptance-red.tar.gz
SHA-256:
  E847D771177E254548A33156394CB43578B79BD3A6E420842B437A161DD49664
behavioral-red log SHA-256:
  6B91C1FCEE29C953C56336F5CE18BCB1214A2C713379FCDCBCFD3ECF658CB156
gofmt log:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R94 增加一个纯 acceptance fixture：baseline burst goodput `100/100`、TPS-floor violation
`0.1 s`；candidate goodput `70/70`、violation `0`。旧 validator 错误通过，测试干净失败于
“candidate removed an allowed TPS-floor tick”不能掩盖 burst goodput regression。该 red 可编译、
无 runner/fixture 错误。随后 active source 已把 `burst` 加入 1% bounded-goodput gate，并回退
unknown-TPS single-speculation；尚待新 exact-source simulation green。

### 13.41 R95 corrected deterministic simulation green

```text
R95 exact-source archive:
  tmp/pig-v011-request-aware-r95-corrected-simulation.tar.gz
SHA-256:
  5DD4C0A673459047CDE1036FA1F44706A4DB206C259A234C73C92F39F0253AEE
packages log SHA-256:
  DDFA457A23914D34F2E86ED361DD2C6204A43F7D75650583B19A0B360211B2D0
report-1/report-2 SHA-256:
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
gofmt/stderr:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R95 在回退 arbitrary unknown-TPS cap 后，runtime/config/metrics/server/simulation/cmd packages
全部通过，双次 replay byte-identical。`pre-poll-burst` candidate 与 baseline 均 admit `5/5`，
completion/SLO tokens/s 同为 `102.405/101.571`，TPS-floor violation 同为 `0.1 s`，新 burst
non-regression gate 生效。suite aggregate candidate completion/SLO tokens/s 为
`98.485/98.398`，baseline 为 `80.810/80.724`；preemption 均为 `1`，TPS-floor violation 均为
`0.2 s`，waiting 均为 `5.0 s`，demand idle 均为 `0`。这是合成模型的 focused simulation
evidence，不能外推真实 GPU/生产提升；R95 后又追加计划证据，且尚未完成三轮 review、benchmark
和 full/vet/race/build，因此不是最终 release archive。

### 13.42 第六轮计划合理性与过度设计复查

本轮只复查第 1--12 节、current checklist 与账本尾部，发现并修正四处 active-contract 漂移：

1. counter reset 条款仍残留已被 R93 否决的“每 snapshot 单次 speculation”，现改为与当前源码和
   R95 一致的 TPS unknown-neutral；安全继续由 hard KV、waiting/KV pressure、reservation 与下一
   500-ms snapshot 提供；
2. current checklist/summary 仍写 unknown-TPS rule“待回退”和 R95“尚待”，现记录 R94 red、
   已回退源码与 R95 focused simulation green，同时明确它们已被后续源码/文档变化 supersede；
3. release 顺序把 legacy production-reachability 审计放在 final exact-source matrix 之后，可能在
   matrix 后再次改源码并制造 false green；现将审计并入三轮 review，只有不再产生源码变化后才
   封存唯一 archive 并跑最终矩阵；
4. “删除旧学习实现”的表述过宽，且 SOLID 边界把 Manager 与 reporting 混成一个职责。现限定为
   删除 v0.11 production 可达消费，不为不可达 legacy tests/modes 大删数千行；职责明确为
   estimator、observer、pure policy、Manager、thin transport/reporting adapter 五部分。

本轮没有新增 learner、cache、exact tokenizer、queue、route、sequence cap、参数或接口层；最小
逐请求算法和 R95 acceptance thresholds 不变。最新新增的 concurrent rebase test 尚未取得
builder/gofmt/race 证据，因此本轮只完成计划修正，不提高 source/release 完成层级。

### 13.43 R96 concurrent rebase focused/race green

```text
R96 exact-source archive:
  tmp/pig-v011-request-aware-r96-concurrent-rebase.tar.gz
SHA-256:
  510167FEC675DD4DC90F298AC83EAEF617960D4B2B78F7073BB181F819ABEC44
builder:
  cvm_3e2k83KX / pig-v01011-builder / Go 1.24.13 linux/amd64
focused count=10 log SHA-256:
  4EE292B845E803115C38891361C1D6D7538BE083CA40D5EE0B09E543E88D4424
focused race log SHA-256:
  687BA8ABB7BEF616C2D640C517DBD9CBBB9ACFB976533C568823E2EB749EC7E5
gofmt log:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R96 在 1/16/64/256 个 old-epoch reservations 上并发执行 forward/terminal 与 `RebaseEpoch`；
最终 intake reopen、reservation/retired 均为零、virtual base 精确等于 reset sample，且所有旧 ID
再次 forward/terminal 均失败。focused test 重复 10 次与 focused race 均通过。该 fixture 与
R96 archive 仍不替代 package/full/vet/full-race/build/simulation/benchmark；本节追加后 canonical
文档已与 R96 archive 不再 byte-identical，因此最终 release 仍必须封存新的 exact archive。

### 13.44 R97 low-flow completion-window behavioral red

```text
R97 exact-source red archive:
  tmp/pig-v011-request-aware-r97-low-flow-completion-red.tar.gz
SHA-256:
  0CB3D4E5985F73F7C3F07D570DED7F312BC1948C23FC98ECBA428D716BB879CF
observer producer log SHA-256:
  5CB49D1FCFDC7AF31CC9C1F33148F32B3BFB97DFEC1D391567DD9BBF3177690F
pure-policy red log SHA-256:
  AF9881056A63CB96964C6693AC56DBEF045F25F63CF26FF4C7C803775811A7B2
adapter red log SHA-256:
  A9140A2483E3CD99AA957077173B7D79C9BAEF43A7D1F990109518CE07063CE4
gofmt log:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R97 producer fixture 证明 observer 会合法发布 `previousRunning=1 -> currentRunning=0`、
generation 增长得到的 fresh TPS proxy；旧 pure policy 随后错误返回 `hard_protect/invalid`，adapter
真实映射为 pre-forward `request_reject/predictor_profile_unknown`。两个 red 均失败于预注册的
low-flow verdict，而非 compile、runner、fixture 或 formatter。active source 已采用最小修复：
当前 `effectiveSequences==0` 时 TPS 对 admission neutral；出现本地 effective sequence 后仍使用
aggregate proxy 做 post-admit forecast。尚待 focused/package/race/simulation green。

### 13.45 R98 low-flow completion-window focused/package/race green

```text
R98 exact-source archive:
  tmp/pig-v011-request-aware-r98-low-flow-completion-green.tar.gz
SHA-256:
  DE1FEA14241A5FDD5E6E7305E5C77384359F27532ED3DEF2AA5372E6C97B45B2
runtime focused count=10 log SHA-256:
  1B42F7FF94FA01A5D834EDF604A1FEB8C2C4E2A9641039E02430E1229EC1075E
server focused count=10 log SHA-256:
  29742C3EDD72A1BAA60AE3A13BDA81CB41C32E0AEC90842E2CBD5353B1858E4B
runtime/server package log SHA-256:
  61F5B2A988E01A5B65A050847613F9606CD2E1D1E1C10228215A2B0B855E874F
focused race log SHA-256:
  9263B5117E1AA414787387A0B16D52EA0185EC848F200C879BBD702E7E13A491
gofmt log:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R98 证明 idle completion snapshot 的第一个 hard-fit request 以 TPS-neutral OPEN 进入；同一
snapshot 累积 local reservations 后，aggregate proxy 会重新进入 `effectiveSequences+1` forecast，
不会变成无限 optimistic burst。observer/policy/adapter focused tests 各重复 10 次，runtime/server
packages 与 focused race 通过，并同时复验 R96 concurrent rebase。该 green 尚未包含修复后的
deterministic simulation、benchmark 或完整 release matrix；本节追加后仍须创建新 exact archive。

### 13.46 R99 low-flow fix deterministic simulation green

```text
R99 exact-source archive:
  tmp/pig-v011-request-aware-r99-low-flow-simulation.tar.gz
SHA-256:
  A310525EE925B23B108FB6C714CF89ECAF64A7E2BF2C6943FA2C958DB268C4CD
packages log SHA-256:
  19C8939B2AD925D9BAA691D898DC04A5CE7EEAC741A863CB063BACF6C804238B
report-1/report-2 SHA-256:
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
gofmt/stderr:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R99 双次 replay byte-identical，且报告与 R95 byte-identical：candidate aggregate completion/SLO
tokens/s 仍为 `98.485/98.398`，baseline 为 `80.810/80.724`；preemption 均为 `1`，TPS-floor
violation 均为 `0.2 s`，waiting 均为 `5.0 s`，demand idle 均为 `0`。`pre-poll-burst` 仍为
`5/5` admits、`102.405/101.571` completion/SLO tokens/s、`0.1 s` floor violation；
`low-flow-first-large` 两策略均 `2/2` admits 且无 self-lock。R99 证明修复未改变现有 synthetic
suite 结果，但不证明真实 GPU/production 提升，也不替代 benchmark 或 full matrix。

### 13.47 R100 pure-policy KV margin behavioral red

```text
R100 exact-source red archive:
  tmp/pig-v011-request-aware-r100-policy-kv-margin-red.tar.gz
SHA-256:
  8C01F238E68E82D05B54D19827E041BD74C3DAC50238C676636F870F5C7D9455
behavioral-red log SHA-256:
  254B3AD6A186848813CAB0541FEEB573986F675AE3F7BC1B35B2A8DCBE011EC1
gofmt log:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R100 证明 production outer config 虽不可达 `soft=0`/`hard=1`，旧 pure policy 自身却同时接受
二者，违反 active `0 < soft < hard < 1` margin contract。red 编译且干净失败于两个预注册值，
不是 runner/formatter failure。active source 已把 Validate 边界收紧为 `soft>0`、`hard<1`；
尚待 focused/package/race green。

### 13.48 R101 pure-policy KV margin focused/package/race green

```text
R101 exact-source archive:
  tmp/pig-v011-request-aware-r101-policy-kv-margin-green.tar.gz
SHA-256:
  CF59014392522491E15014DC1741040927DBF239F37FEC3777083E0B2C7C637D
focused count=10 log SHA-256:
  BE9629CE04B46E2E0CB66C28B9F34615D61B541F9DCD78545A516AC023F55805
runtime package log SHA-256:
  F2F5A19DE68781CEDB0C0E39627064368753164D7AE8DE679B6680149187BC59
focused race log SHA-256:
  625773C2999CFD5E3967403E225BB97EBF53AAAD044C0AC9BF0A1C0193A52FF7
gofmt log:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
```

R101 focused test 重复 10 次、runtime package 与 focused race 通过。该修正只收紧 invalid
configuration；production defaults/valid configs 的 verdict 不变。R101 之后追加本节与三轮 review
结论，因此仍不是最终 release archive。

### 13.49 第六轮三轮实现复查结论

**第一轮：model/causality，修正后通过。** Production factory 只构造
`RequestAwarePolicy + Manager(scheduler=nil) + VLLMObserver + request-aware adapter`；manifest 为
`model-agnostic-json-v1`，没有 tokenizer asset、model profile、cache input、learner 或 TTFT/TPOT
gate。真实 proxy 在选择 upstream 前完成 bounded classification、policy decision 与 atomic
reservation；同 snapshot 改变 approximate input size 会改变真实 pre-forward verdict，OPEN 对
hard-fit large request work-conserving。审计发现 completion-window TPS 在 `currentRunning=0` 时被
policy 误判 invalid，已由 R97/R98 修正；R99 simulation 未出现 goodput/QoS regression。

**第二轮：safety/lifecycle，修正后通过 focused 层。** Manager 在同一锁内读取 virtual state、
evaluate 与 reserve；sample reconciliation、terminal 与 rebase 都串行化。adapter 的 close/forward
锁序为 adapter -> Manager，observer 在调用 Manager rebase 前释放 observer 锁，未发现反向锁序。
same-identity counter reset 先使 observer snapshot fail closed，再清空 old reservations/retired、
替换 exact base 和 reopen；identity/capacity/block drift 只 invalidates，不走 rebase。R96 覆盖并发
old handles，R75/R76 覆盖 close/forward；stale/preemption/hard KV、shadow no-side-effect、Router
inspect no-reservation 已有 focused tests。每个 reservation 至少占一个 block且受 hard KV bound；
retired queue 固定 4096；observer/adapter/reporting 只有 fixed snapshots/counters，没有 prompt/model/
request ID label。审计发现 pure policy 可独立接受无 soft band 或 hard margin 的配置，已由
R100/R101 修正。完整 full race 仍是 release gate，不能由本轮静态 review 替代。

**第三轮：SOLID/evidence/release，通过进入 final matrix。** 职责保持 estimator、observer、pure
policy、Manager、thin transport/reporting adapter 五部分；没有为函数级抽象新增接口或第二套
reservation registry。静态 production reachability 中 `newApproximatePredictiveShadow` 没有
non-test caller；shared observer 的 learner fields 在唯一 production factory 均为 nil，不能改变
v0.11 verdict。enforce 跳过 legacy queue/tier/lane/backend-priority，配置要求单一 backend；旧
dynamic snapshot 只保留 raw telemetry，Router-consumed waiting/global limit 由 request-aware
projection 覆盖，coarse status 同样读取该 projection。README 的 v0.11 section 与 active contract
一致；Compose 示例继续引用真实已发布的 v0.10.13 image，未虚构 v0.11 artifact。R96/R98/R101
只是 focused/package evidence，R99 只是 synthetic simulation；没有 commit/push/tag/image/deploy/
live evidence。下一步必须在一个新的 exact archive 上执行 benchmark 与
full/vet/full-race/build/simulation，且不把旧的两个非-canonical v0.11 plan 文件加入 release
contract。

### 13.50 R102 exact executable-source final matrix green

```text
R102 exact-source archive:
  tmp/pig-v011-request-aware-r102-final-matrix.tar.gz
SHA-256:
  C7607359E5738B88D92F4164111D577AAA2C1F140CF01F41B22DF142AFF5DE6B
builder:
  cvm_3e2k83KX / pig-v01011-builder / Go 1.24.13 linux/amd64 / CGO_ENABLED=1
executable-source manifest SHA-256:
  E5AF955F25B4E567EED7A7DB58F0A1DE17F2C9AFD0E0824D3EEB10B74456BD8E
full test log SHA-256:
  F251E929E31421D61BE6CC683CEBB8A79875AA668C99B7842648A06B9E170CAB
full race log SHA-256:
  C4DBB901D488BB56A6E54607260DEA0C805D08FB5B3C428B6E15D4B7484534E2
gofmt/vet/build logs:
  empty / E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855
policy benchmark log SHA-256:
  22259EF919E68EC5CBB52085F68DF1610CEA0DE763F7B20E261D43C5163C6E7E
estimator benchmark log SHA-256:
  CD92D9925449966E2BECC8C9504422293C24979BDD94F3E51F2F5A48151B975B
HTTP fixture benchmark log SHA-256:
  8EDDAE181BC3BAE4AA88F36E0769143EC7B721C9B8A5FDABA1C67AA13F57940C
report-1/report-2 SHA-256:
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
  D4E36D18BE66ADC45CFF4657DE8219DCFCA2EE67250F5CC98B7E5C714A227A2C
```

R102 在同一 archive 上通过 whole-repo `gofmt -d`、`go test ./... -count=1`、`go vet
./...`、`go test -race ./... -count=1`、`go build ./...`、三组 benchmark 和双次 byte-identical
simulation replay。关键 benchmark（五次范围）为：

- pure policy OPEN `29.43--30.02 ns/op`、SELECTIVE `31.90--34.55 ns/op`、hard-KV
  `26.98--28.02 ns/op`，全部 `0 B/op, 0 allocs/op`；
- full estimator：1 KiB `273.1--280.2 ns/op`、64 KiB `1.970--2.007 us/op`、2 MiB
  `65.114--67.026 us/op`，全部 `0 B/op, 0 allocs/op`；
- bounded lexical hint：1 KiB `90.50--91.40 ns/op`、64 KiB `90.38--92.42 ns/op`、2 MiB
  `88.07--90.16 ns/op`，全部 `0 B/op, 0 allocs/op`；body-size throughput 数字不表示全量扫描，
  因为 hint 只采样固定窗口；
- HTTP pre-forward fixture `44.136--46.568 us/op`、约 `39.3 KiB/op`、`119 allocs/op`；它包含
  `httptest`、request/response/classification，不是 pure policy 或生产链路增量 latency。

simulation 仍为 candidate aggregate `98.485/98.398` completion/SLO tokens/s 对 baseline
`80.810/80.724`，preemption `1/1`、TPS-floor violation `0.2/0.2 s`、waiting `5.0/5.0 s`，
acceptance passed。R102 后只追加了本节 non-executable ledger；Go source、Dockerfile、go.mod/go.sum
未改变，R102 executable-source manifest 仍适用。当前完成层级为 source implementation + complete
builder matrix；尚无 commit/push/tag/image/Compose/deploy/live evidence。

### 13.51 R103 source tag 与 immutable production image green

```text
source commit:
  d88b598f9bc57af2ca71eab1879a56d6e1406422
branch:
  pig-origin/codex/pig-v0.11.0-request-aware
annotated tag object:
  fa7e54188693f245183c3b80f276087a96a946f9
tag dereference:
  v0.11.0^{} -> d88b598f9bc57af2ca71eab1879a56d6e1406422
builder:
  cvm_3e2k83KX / app 89811a9add5b20427ee1fbf4dc22a33984e41959
  pig-v01011-builder / Go 1.24.13 linux/amd64 / restart=0 / OOMKilled=false
R103 builder directory:
  /var/volatile/dstack/persistent/pig-builder-work/pig-v011-release-r103-d88b598f
builder-local image ID:
  sha256:fbf8b104364e73ee7cc8feb3098c1fe6dc9197a9c4b38c8d38f4ddbf804dda93
registry immutable image:
  ghcr.io/phala-network/phala-inference-guard@sha256:6fa3b1dde11ab3ccbd4e88df9e5c7abf76a7fb255703da5ebc03cec01e0eb110
pulled registry image ID:
  sha256:e5f38b5dcbad99323b5dfbb43304355eb8cc00f1200bd3375994b631b8618952
builder-local / registry binary SHA-256:
  202af72781badf854ac0dca41c1c0e6a0e055a3a730e2768d121ac8343028385
local image contract log SHA-256:
  61345da72ae62e76aaa512649df4c9a5dd42e2ce926e3088fb27ca58c9c9cd7c
immutable pull log SHA-256:
  895cb40bbe7fae350c54fb3917e7d66172c43bd524980da7b87cacba94bb7b85
immutable image contract log SHA-256:
  b9373f8cf77218436ee60b2782117ec4069e65dd769fdc684987ffa91e7616a7
immutable binary evidence SHA-256:
  f75c1d759ea11ca90c4350f9629a227f6b71b4a2b902589967656c34fcd6e566
```

R103 从公开 `v0.11.0` tag 重新 clone，验证 clean HEAD 精确等于 R102 后提交的 commit，再独立构建
production image。builder-local image 通过 `validate-production-image-contract.sh`，为 linux/amd64、
label `0.11.0`、`NVIDIA_VISIBLE_DEVICES=all`、distroless entrypoint `/phala-inference-guard`，二进制包含
native NVML collector 与 `PIG-v0.11.0`。

准备 push 前的 authenticated manifest guard 发现 registry tag `v0.11.0` 已存在，因此 R103 没有
覆盖 tag。现有 tag pull 得到 immutable digest `6fa3b1dd...`；按 digest 再次 pull、执行 production
image contract 并提取二进制，结果与 R103 从精确 source commit 构建的二进制 SHA-256 完全相同。
两次构建的 Docker image ID 不同不作为源码差异证据；生产可执行文件 identity、version、label、
entrypoint、NVML contract 与 immutable digest 均已独立验证。用于 GHCR 的临时 Docker config 已
logout 并删除；一次错误把 `--version` 当作退出型子命令启动的临时容器也已精确删除。

R103 只完成 source tag、builder-local image 和 registry immutable image 层，没有修改 Compose、
CVM、Router 或生产流量。下一步必须重新读取 `use1-cb` live Compose 与 route state，再执行
Router-disabled shadow；不能由本节推断 deployment readiness 或 live goodput。

### 13.52 R104 `use1-cb` Router-disabled shadow deployment/readiness green

```text
authorized target UUID:
  a0f0bfb3-e46f-4b22-814e-24872f251193
live CVM/app/instance:
  cvm_EKvOJ1wb
  8dd15fa9cce98fa3e466533e442164a407542d95
  b16a2441e21aa6dc0428d21802cb5bad872dafb9
pre-deploy Compose SHA-256:
  49260a303b5f7a1b02c1d71f8ae5fa95c6bb27ec0068229177ebeded9905c27b
shadow candidate/live Compose SHA-256:
  a798b5136fdaef32979ae7cf991908fe9432e27de72b15867d66e587a711a930
PIG immutable image:
  ghcr.io/phala-network/phala-inference-guard@sha256:6fa3b1dde11ab3ccbd4e88df9e5c7abf76a7fb255703da5ebc03cec01e0eb110
PIG image ID:
  sha256:e5f38b5dcbad99323b5dfbb43304355eb8cc00f1200bd3375994b631b8618952
Router config digest:
  sha256:b8447a719c6fb9d8bf956ae60928d5e857828e7c2874739e7bb8fae8cca5c47a
Router enabled set before/after shadow:
  use1-9b, use1-19
use1-cb Router enabled:
  false
```

第一次显式传 `-e .env` 的 deployment 在提交前被 centralized KMS 的
`CVM encrypted_env_pubkey is required` 拒绝；live Compose hash、`in_progress=false` 与进程检查
证明它没有产生 mutation。随后复用 CVM 已有 sealed environment、只提交 candidate Compose，
`phala deploy --wait` 成功；部署后 live Compose 的 UTF-8 byte SHA-256 与 candidate 完全一致。
Router upstream config/digest 与 enabled set 未改变，没有引入 Router 流量。

本次完整 CVM update 使 vLLM 重新启动。vLLM 在 `2026-08-06T04:27:07Z` 报
`Application startup complete`，启动指标确认 KV capacity `862437` tokens、block size `64`。
PIG 第一次启动从约 `04:21:23Z` 等待到五分钟 probe 上限，并在 `04:26:23Z` 因 vLLM 尚未监听
而退出一次；`restart: always` 第二次启动在 vLLM ready 的同一秒成功，随后持续稳定。PIG container
ID 始终为 `d234c263...`，最终 uptime 比 vLLM/HAProxy/ingress 少约五分钟；没有连续 restart loop。
这是 Router-disabled 启动期受控 retry，不作为 steady-state crash green；enforce 部署仍必须重新
验证只发生至多一次同类 retry，ready 后不得再 restart。

最终 readiness 证据：CVM `running/in_progress=false`；PIG、vLLM、HAProxy、ingress 均 running；
authenticated `/v1/models`、`/pig/metrics`、`/v1/metrics`、`/v1/attestation/report` 均为 `200`，
model 为 `google/gemma-4-31B-it` 且 NVIDIA attestation payload 非空；两个 metrics endpoint
unauthenticated 均为 `401`。PIG metrics 为 `PIG-v0.11.0`、predictive mode `shadow`、KV admission
`off`、dynamic TTFT protection disabled、capacity `862437`、intake open、reservation/retired/preemption
均为零。当前 boot 的 PIG/vLLM 日志未发现 CUDA OOM、EngineDeadError、traceback、NVIDIA Xid、
panic、fatal 或 killed process。

live Compose 明确设置 `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS=500` 与
`PREDICTIVE_MAX_METRICS_AGE_MS=1500`；R103 binary identity 对应的 production factory 把二者分别
接入 request-aware observer 的 `PollInterval`/`MaximumAge`。`pig_dynamic_poll_total` 和启动日志中的
legacy `poll=100ms` 属于旧 dynamic/queue observability，不是 request-aware observer cadence，不能
用来支持或否定 500-ms 合同。

### 13.53 R105 Router-disabled shadow protocol/low-flow/lifecycle green

对 direct target endpoint 执行 26 次 admitted-path attempt，代表协议结果：

```text
simple chat:       HTTP 200 / 1997.4 ms
stream chat:       HTTP 200 / 551.7 ms / SSE [DONE]
tool call:         HTTP 200 / 593.1 ms / get_weather
JSON schema:       HTTP 200 / 1473.2 ms / {"answer":42}
Responses API:     HTTP 200 / 572.6 ms / completed
large input chat:  HTTP 200 / 1863.8 ms / 69077 prompt chars
```

同一健康状态下真实 pre-forward cost 随输入变化：简单请求为
`selection_input_tokens=23/reserved_tokens=128`，约 69k-char 请求为
`selection_input_tokens=14585/reserved_tokens=34624`；两者均为 `ADMIT/open`，证明 approximate
request size 已进入真实决策输入，又没有在健康低流时对 large request 设置额外软门槛。

低流/完成窗口与 burst：idle 约四个 observation 周期后，72,049-char 的首个 large request 为
HTTP 200/`ADMIT/open`；两轮 decode request 完成后立即发送下一请求，均为 HTTP 200，下一 verdict
均为 `ADMIT/open`，running/waiting/reservation/retired 回零；四路 small burst 全部 HTTP 200，
drain 后 preemption 增量为零。burst 中 shadow 曾产生
`SIZE_PROTECT/request_size/tps` would-verdict，但请求仍 forward；同一事件在日志与 metrics 中的
action/reason/source 一致，且 shadow Router backpressure 始终为零、raw/effective global limit
保持 `50/50`，没有把 would-verdict 变成外层 clamp。

合法 streaming disconnect 使用 .NET HTTP client 在收到 `975` bytes 后主动取消；PIG
`pig_client_disconnects_total{phase="response"}` 与
`pig_client_disconnect_upstream_cancellations_total` 各精确增加 `1`，随后 running/waiting 回零，
preemption 不增加。vLLM 的 `request_success_total{finished_reason="abort"}` 没有增加，因此不把该
backend counter 伪报为 green；本 gate 的直接证据是 PIG 已识别 response disconnect 并取消 upstream。
shadow 按合同不创建 enforce reservation，最终 reservation/retired 都为零。

两次 Windows `curl --data-binary` 取消夹具因 native argument quoting 破坏 JSON，产生
`HARD_PROTECT/invalid`；逐请求日志明确为 `selection_input_tokens=0/reserved_tokens=0`，且请求得到
4xx。该 malformed fixture 不计算法误锁证据。改用 .NET 发送合法 JSON 后 recovery request 和
disconnect request 都为 `ADMIT/open`。最终现场为 attempts `26`、intake open `1`、running/waiting
`0/0`、reservation/retired `0/0`、preemption `0`、predictive/backend failure metrics 全零，last
decision `ADMIT/open`；Router enabled set 仍为 `use1-9b,use1-19`，`use1-cb=false`。

R105 证明 shadow compatibility、low-flow/self-lock、完成窗口、取消和小 burst，没有证明真实
enforce 429 不进入 vLLM，也没有在自然压力下证明 small/large 同 snapshot differentiation 或生产
goodput。下一步只能在 Router 仍 disabled 时部署唯一行为差异为 `shadow -> enforce` 的 candidate，
重跑全链 readiness 后用受控请求证明 pre-forward protection、统一 observability、exact-once
reservation 和 non-sticky Router projection；不得由本节直接 enable Router。

### 13.54 R106 v0.11.0 Router-disabled enforce readiness 与长 Prefill corrective

v0.11.0 enforce candidate 已部署到唯一授权的 `use1-cb`，live Compose UTF-8 SHA-256 为
`d6458ac3baf8d46f59a3ad6789708b63c2fa4602fc6ef96ab4e09509c763192e`。2026-08-06 本轮只读
复核时 CVM 为 `running/in_progress=false`；PIG、vLLM、HAProxy、ingress 均 running，PIG/vLLM
当前启动分别约 16/22 分钟。PIG 当前日志为 `PIG-v0.11.0`、mode=`enforce`、intake open、
reservation/pending/retired=0，当前启动窗口未发现 panic/fatal/OOM/EngineDead。vLLM 当前启动在
`05:03:47Z` ready，capacity 仍为 862,437 tokens；日志中 2026-08-05 的旧 traceback 不属于当前
boot，不能混入本次 crash 判断。

Router authenticated read-only snapshot 仍为：enabled set `use1-9b,use1-19`，`use1-cb` 存在且
`enabled=false`。因此 enforce readiness 没有引入 Router 实际流量，也不构成 canary。

随后源码 reachability 复查发现：`requestAwareAdapterCost` 已把 `EstimatedInputHigh` 写入
`RequestCost.UncachedPrefillUpper`，Manager reservation 在 `MarkPrefillComplete`/terminal 前也会
持有 pending-prefill tokens；但 `RequestAwarePolicy.Evaluate` 完全不读取这两个已有状态。
它在 KV/TPS/waiting pressure 都为零时无条件 `ADMIT/open`。所以即使多卡大模型允许 512K/650K，
单个 cold prompt 也会先进入并产生几十秒 Prefill 干扰，PIG 只能等后续 snapshot 反馈性保护。

该盲点违反 pre-forward 预测目标，故 v0.11.0 promotion 在本节明确暂停。R104/R105 的协议、
低流和生命周期证据仍有效，但不证明长 Prefill QoS。下一 executable version 为 v0.11.1，按
第 5.5 节先补 behavioral red，再复用 Manager 原子 lifecycle 实现 token-weighted gate；不得在
当前 v0.11.0 enforce 上启用 Router，也不得用当前 262K max-model-len 删除通用 512K/650K 测试。

### 13.55 R107 长 Prefill deterministic simulation 首轮 red

本轮 exact working-tree archive SHA-256 为
`8b8af0eea187eb9de35eeb6476fa86258b0a8e9608c742bb301b9a85b7eca370`，只在授权
builder `pig-v01011-builder`（Go `1.24.13 linux/amd64`）执行
`go test ./internal/simulation/requestaware -count=1 -v`。idle 650K、busy 650K、
200K+100K budget、300K singleton 和 Prefill-complete recovery 均符合预注册结果；唯一失败是
`prefill-quiescent-cancel-recovery`，log SHA-256
`633ae8a9ee3c96639cfebc1f53bd372d482755ad8a71b5df746b76005f148232`。

失败原因不是 reservation 泄漏：取消在 1.1 秒删除了本地 active request，但 1.0 秒的 vLLM
snapshot 仍为 `running=1`，1.2 秒的另一个 650K 因 `prefill_busy` 被保护。旧预期错误地把“本地
terminal 已删除”当成“上游已经停止”。active contract 已修正为：terminal 立即释放本地 gate，
但不能覆盖 busy snapshot；默认 500-ms 下一 poll 确认 idle 后恢复。测试现同时发送 1.2 秒旧
snapshot 重试和 1.6 秒 post-poll 重试，要求前者保护、后者放行，最大 idle-with-demand 不超过
一个 poll。

### 13.56 R108 长 Prefill simulation/package/race green

修正后的 archive SHA-256 为
`70ac01632b9d71d1307960dfbc8685dba43c2c562cdf54600b6edd1ea16e935c`。builder 上
simulation 与 runtime package 通过，log SHA-256 分别为
`c861c176e926ab50e19c6841ce5e9d0470af6ea8dbd2dd27f7ef710deb2a3f4b` 与
`a759f9924ef982f248951611c2270641b495b085a762ef84377e28afb2116fcb`。随后五个 changed
package 和对应 race 全部通过，logs SHA-256 为
`a9662dca0dda1e255278d7714f50d609dd97b900802b9f402061c739ddfe5252` 与
`9170dda2c6676d5eb3bf1372a04dce7c228b3cb1015eb4772db9732a1def9ebb`。

这层证据确认：idle 首个 650K 不自锁；busy 650K 在转发前保护且 Decode SLO goodput 不差于
baseline；200K+100K 受 256K aggregate budget；两个 300K 只进入一个而 32K 仍进入；取消恢复
不超过一个 poll；Prefill 完成后的普通请求在下一 poll 前恢复。它仍是 source simulation/package
证据，不是 image、deployment 或 live goodput 证据。

### 13.57 R109 request-aware Manager 单次扫描效率复查

在 archive SHA-256
`4201edbfe873adc84ea8a3dcd46b5e5ba9badcef2fa4119b3148a2fe78a0ade3` 上建立基线：pure
policy 为 `18--46 ns/op, 0 alloc`；Manager 0/48/256 active reservations 分别约
`160 ns / 5.5 us / 31.4 us`，benchmark log SHA-256
`574e525bd64951f3a4fbed3ed7132aa56fe3e06661deb56615055896eb904b3c`。审计确认 Manager 先为
virtual state 扫描 map，又为 long/quiescent count 扫描第二次。

没有引入需要在每个 terminal/reconcile/rebase 路径维护的 O(1) counters。源码只提取唯一
`addReservationToStateInterval` helper，并在 request-aware Manager 的同一次 map scan 内派生
virtual state 与 bounded long/quiescent counts。优化 archive SHA-256
`e0b7a9318e3a89b4504eb2a7ffe1d64a211ad4b17e5800be336650f93d5f410e` 的 changed packages、race
和 benchmark 全通过，log SHA-256 分别为
`3caa3377b5534e89e51ca7a2ef1e701fdb889f346b1548d3af12e3d0866e624b`、
`6d8d38b80a50311532bf1f29d2c53a147982182e299584bb725a1d4b1500d953`、
`387550f0a95ed10841f0ae4be9423fb571730faf4b9615f30a11073996cb0268`。48 active 降至约
`4.9--5.0 us`，256 active 降至约 `25.9--26.8 us`，仍为 0 alloc；该结果只证明 CPU hot-path
改善，不推断 GPU goodput。

### 13.58 R110 第一轮因果复查：本地 Decode 阻止第二个 650K

因果复查发现 quiescent gate 只读取 observed `running/waiting` 和 pending Prefill，漏读 Manager
已经计算的 `EffectiveSequences`。首个 650K 在 snapshot 尚为 `running=0` 时完成 Prefill 并进入
本地 Decode reservation 后，第二个 650K 可错误 `ADMIT/open`。active contract 先增加
`local effective sequences=0` 条件，再封存 behavioral-red archive SHA-256
`ac128d66449d7512fe5e48306bb98500021942cbd9d74ae6b0f942d4a5f908d0`。

builder runtime red 同时在 pure policy 与真实 Manager 命中错误 `ADMIT/open`，log SHA-256
`8a1fff459e9a5fbd07847da797d69c931d4c7fe2e2f39ef3aedbd8e1c9be3f15`。650K 实际 Prefill
远长于 poll，因此 simulation 已从 observed running 得到同一安全 verdict 并保持 green；其 log
SHA-256 `9ec677ef6c3386cfe3182ead67284d7d60081a843c23d05164bfa4a36c88257d` 不冒充该亚
500-ms 窗口的 behavioral red。

最小修复只把 `EffectiveSequences>0` 加入 quiescent busy 条件。green archive SHA-256
`c0feaefad6e8cf7e3bb34148f5a33e20577f552b0c1fd1c5d8e0c3e0c47a875a` 的 runtime/simulation
focused 与 race 均通过，logs SHA-256 为
`3040e1321436c8909b7d59375ff108a985f4042ca7e864dd80b2bbc140c54bee` 与
`d060e463ccac53b4747ef35015d21f05826cad7289eeb200520d95c6d04a1627`。因此 Prefill 完成后
普通请求仍立即恢复，第二个 512K/650K 则等 observed 和 local active work 都归零。

### 13.59 R111 三轮复查与 exact-source final matrix green

三轮复查结论：

1. **模型与因果**：快速输入估算的 `EstimatedInputHigh` 进入真实 `RequestCost`，并在 Manager
   原子决策前变成 `EstimatedPrefillTokens`；64K/256K/512K/650K 可在同一 backend snapshot 下
   改变 pre-forward verdict。复查发现并修正了 R110 的 local Decode/第二个 650K 缺口；没有
   cache 命中信息时继续明确使用保守总 prompt estimate，不虚称精确 uncached tokens。
2. **安全、生命周期与 SOLID**：hard KV/stale/preemption 仍先于 size gate；check+reserve 在唯一
   Manager 锁内；pending/quiescent 从同一 reservation registry 派生，terminal/rebase/Close 不维护
   第二套 counter；取消不覆盖 busy snapshot，Prefill complete 只解除 Prefill 独占而不伪造完全
   idle。single-scan helper 删除重复遍历但没有增加 lifecycle state。focused/full race 均通过。
3. **证据与发布**：HTTP 429、backend call=0、domain reason、request-aware reason/source、日志、
   bounded-label Prometheus 和 Router inspect capacity 已由同一 verdict 的 integration test 覆盖；
   deterministic simulation 包含 idle/busy 650K、weighted budget、300K singleton、cancel、
   completion-before-poll 和低流自锁。source/build/image/deploy/live 层继续分开，不继承 v0.11.0
   image 或 deployment 证据。

最终 executable-source archive SHA-256 为
`1aa0c933d274ad192ec037399d2162d052b9879550ee96253eadd8f6309d9118`，builder 为
`pig-v01011-builder`、Go `1.24.13 linux/amd64`。在该 archive 上：

- whole-repo `gofmt -d` 为空，SHA-256
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；
- `go test ./... -count=1`、`go vet ./...`、`go build ./...` 全通过，logs SHA-256 分别为
  `011f7c5416b9014c5a1e27f8b7c338d7821fbba63581d3f661269cf6a90fa042`、空文件 hash、空文件 hash；
- `go test -race ./... -count=1` 全通过，log SHA-256
  `895242ec95cda9bdb6ecc8131d0c55570c366bc1d0d29deeffff8037735a4d20`；
- 两次 `go run ./cmd/pig-request-aware-sim` 均为 46,911 bytes、acceptance=`passed`、byte-identical
  SHA-256 `182c23680a7902c67e54e1543558efb74ee96451fda375623bbfb1c6736b205b`；
- estimator、policy/Manager、HTTP benchmark logs SHA-256 分别为
  `032aacd124796d710155278a569d1c036230f6fe9aae3ef20ff5b8c9cd6967bf`、
  `62834419ec542e139fccedd3ffbf6ebf6280d8bdfc5409a059ccdd75f91b0be9`、
  `8816da77e1b0f07c5fbd0101cc8936d9aad31c580ba97994abc14ba0858a91d0`。

性能结果均为三轮、0 alloc（HTTP test fixture 除外）：Estimator 1 KiB 约 `0.271 us`、64 KiB
约 `1.96 us`、1 MiB 约 `27.6--28.0 us`、2 MiB 约 `61--63 us`；bounded lexical hint 约
`88--91 ns`；pure policy `18--45 ns`；Manager 0/48/256 active 约
`0.164 us / 4.87--4.89 us / 25.7--27.2 us`；完整 HTTP pre-forward 429 fixture 约
`43--46 us`、约 39 KiB/119 alloc（主要是 `httptest` request/response 与 JSON/telemetry 全路径，
不能解释为 pure policy 分配）。这些是 CPU microbenchmark，不是 GPU throughput 或端到端网络
延迟。

R111 只达到 complete clean-builder matrix。追加本节仅修改 non-executable plan ledger；下一步
必须证明 ledger-only archive 与 R111 executable files/binary 一致，再进行目标文件审计、
commit/push、annotated `v0.11.1` tag 和 clean-tag builder image；不能直接部署或 enable Router。

### 13.60 R112 ledger-only source 与 binary 等价证明 green

用户再次确认 512K/650K 分层对多卡大模型有效。因此通用
`65,536 / 262,144 / 524,288 / 262,144` 合同继续保留；当前 `use1-cb` 的 262K
`max-model-len` 只限制该节点能够执行的 live 请求范围，不删除 512K/650K source、生命周期和
deterministic simulation 合同，也不增加任何模型名分支。

授权 builder `cvm_3e2k83KX` 的 SSH gateway 在 KEX 前关闭；确认另一个既有三轮作业完成后，
只重启该无 GPU builder。平台随后恢复为 `status=running`、`in_progress=false`，Compose hash
仍为 `6388efbb0ba0ff8c2be44609344532a01c4207fc14c5196e3e28a9e56daa07a4`。重启造成手工 builder
container 正常以 exit 137 停止，Docker inspect 为 `OOMKilled=false`；重新启动同一
`pig-v01011-builder` 后仍为 Go `1.24.13 linux/amd64`。SSH host key 随 CVM 重启轮换，本轮使用
TLS hostname/chain verification 和新的专用 known-hosts 文件锁定 ED25519 fingerprint
`SHA256:cL6Yhk0milH+/UJcYwy9ebox+uT6HJfMkAKo26pZO3M`，没有关闭 host-key 校验。

在同一干净 builder container 中重新校验两个原始 archive：

- R111 executable archive：
  `1aa0c933d274ad192ec037399d2162d052b9879550ee96253eadd8f6309d9118`；
- R111 账本追加后的 commit candidate：
  `1f71a57abb8791d9f98004950782044528319d453d8ee9d478386250f9e4c953`。

两份 archive 分别解压到新的 `mktemp` 目录，排除唯一 canonical ledger
`docs/PREDICTIVE_MARGINAL_GOODPUT_OPTIMIZER_V0_11_PLAN.md` 后，各有 296 个文件；path、length 和
SHA-256 manifest byte-identical，两个 manifest SHA-256 均为
`9d9b635f27e748e6e4e81b9b7016b71bfa7faf25a0af647db694dcabda29adee`。被排除账本的 SHA-256
分别为 `39cbeb9993c2c47b434eabc109c26075e3e8e70d1f95ed30bc0920466dc40c46` 和
`a67c850fd69f0ee2837f528bb78508543dc917a4c000e69bc71d18f45e73a42b`，证明差异确实只位于账本。

随后在两份 source 上分别执行：

```text
/usr/local/go/bin/go build -trimpath -buildvcs=false -o <r7-or-r8-binary> ./cmd/phala-inference-guard
```

两个 binary 均为 11,744,728 bytes，SHA-256 均为
`650c1ce5ef3c243298a415b788ce4989aef316e0f6aa09b2fdcc918d63632545`，并通过 `cmp` byte-identical。
持久化 evidence 位于 builder
`/work/pig-v0111-r112-ledger-equivalence-20260806`；最终 `run.log` SHA-256 为
`4be5af287426380186e7566a319441ae3ce38267fc2b491fb8c565fb681e97df`，binary hash 文件 SHA-256 为
`39ae9ce2884d691c7569f749e319975814c93725302606833bc02d84096a1b66`。

因此 R111 complete clean-builder matrix 可严格继承到当前 executable source；本节再次只修改
non-executable plan ledger。当前完成层仍是 source implementation + complete clean-builder matrix，
尚无 v0.11.1 commit/push/tag、image、Compose、deployment 或 live-traffic 证据。下一步只能先完成
Git 目标文件审计、commit/push/annotated tag，再从 clean pushed tag 构建和验证 immutable image；
仍不得直接部署或 enable Router。

### 13.61 R113 v0.11.1 production-image contract red 与 v0.11.2 corrective

R112 后完成目标文件审计、secret-like pattern scan、commit/push 和 annotated tag：

```text
source commit:
  4ee38783d515f7598d0e3f9e26ef9b9acd22b467
source tree:
  851880bf279379b621f781782f14545d3245f5d5
branch:
  pig-origin/codex/pig-v0.11.0-request-aware
annotated tag object:
  03191ac190fa8c92c36674c96124017b573d6206
tag dereference:
  v0.11.1^{} -> 4ee38783d515f7598d0e3f9e26ef9b9acd22b467
```

发布镜像前复查发现该 commit 的 runtime binary version 已是 `PIG-v0.11.1`，但 Dockerfile OCI
label 仍是 `org.opencontainers.image.version="0.11.0"`。这会让 source tag、runtime version 和
image identity 不一致，不能部署。已经公开的 annotated tag 不移动、不覆盖；`v0.11.1` 明确降级为
source-only tag，禁止构建、发布和部署。

GitHub `Publish Image` run `31078583638` 对该 tag 的 checkout/login 成功，但
`Validate production image contract` 失败，后续 `docker/build-push-action` 被跳过；builder 对
`ghcr.io/phala-network/phala-inference-guard:v0.11.1` 的 manifest inspect 也返回不存在。因此没有
错误标识的 v0.11.1 registry image，更没有 Compose、deployment 或 live evidence。

corrective release 自主管理为 `v0.11.2`，同时修改 runtime version、README release heading 和
Dockerfile OCI label；长 Prefill 算法、64K/256K/512K/650K 合同与所有默认阈值不变。下一步必须把
这四个 tracked-file 变更封存为新的 exact-source candidate，在授权 builder 上重新执行
full/vet/build/race/simulation 以及 `EXPECTED_VERSION=v0.11.2` production-image contract；通过后
才能 commit/push/tag `v0.11.2`，不能移动 `v0.11.1` 或直接部署。

### 13.62 R114/R115 v0.11.2 exact-source matrix 与 builder-local image contract green

v0.11.2 corrective 的 staged index tree 为
`6ec862486a496eb09aec6db8e49fbe0e760ec071`，封存 archive SHA-256 为
`e0c71374f3f8c2707031f01689f8f648d05e972de8c4d64bcea86fc9363e0dec`。archive 从 Git index
生成，只含 tracked files；两个历史 untracked plans 仍未 stage。host 和 builder container 两层均
重新验证 archive hash。在 `pig-v01011-builder`、Go `1.24.13 linux/amd64` 上：

- whole-repo `gofmt -d` 为空；
- `go test ./... -count=1`、`go vet ./...`、`go build ./...`、
  `go test -race ./... -count=1` 全通过；对应 full-test/full-race logs SHA-256 为
  `f7cb000d9637abc01ee57f96ca03b1718ffb89e88c9ab28f86ec74091a6b5737`、
  `9e9bbbfacb0ad366fcff98f5d7ad4b34f0491e07be9a816c9d729eb563458ecc`，vet/build/gofmt 均为空文件
  hash `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；
- `go build -trimpath -buildvcs=false` binary 为 11,744,728 bytes，包含 `PIG-v0.11.2`，SHA-256
  `478bea65988e7cedfd4cb03d3e1696d4fbc4d0b3b663c10fb726f0764cf35c33`；
- 两次 request-aware simulation 均为 46,911 bytes、acceptance=`passed`、byte-identical
  SHA-256 `182c23680a7902c67e54e1543558efb74ee96451fda375623bbfb1c6736b205b`；
- estimator、policy/Manager、HTTP benchmark logs SHA-256 分别为
  `a4bfaa2bbd4b081b68c7b8247cfcdcaae3c60a217d8777b3555b3bdcd806bc15`、
  `fac7a84595cc4ff73d6392d85a843367eba34ea5115ba0a7bd4efaa149dae6c4`、
  `03a7c1055987266d1da5b4e9b8c9988e6ad4dae1853b2bf128432c22a47399df`。

本轮性能与 R111 一致且所有 pure estimator/policy/Manager 为 0 alloc：Estimator 1 KiB
`271.7--273.9 ns`、64 KiB `1.968--2.001 us`、1 MiB `27.543--27.944 us`、2 MiB
`63.206--63.435 us`；bounded lexical hint `88.34--92.20 ns`；pure policy
`18.18--45.51 ns`；Manager 0/48/256 active 约 `0.164 us / 4.865--4.873 us /
25.688--25.904 us`；HTTP pre-forward 429 fixture `41.686--41.911 us`、39,324 B/119 alloc。
它们仍只证明 CPU hot path，不是 GPU throughput 或网络端到端延迟。

同一 exact source 随后在 builder host 构建
`ghcr.io/phala-network/phala-inference-guard:v0.11.2-candidate-r10`，builder-local image ID 为
`sha256:2323d54212de804dd6964b0c6236f3ffa10002b87d16d3343c7a68fa933ca085`。以
`EXPECTED_VERSION=v0.11.2` 执行 `tools/validate-production-image-contract.sh` 通过：linux/amd64、
OCI label `0.11.2`、`NVIDIA_VISIBLE_DEVICES=all`、distroless entrypoint
`/phala-inference-guard`、native NVML path 均符合合同。提取 binary SHA-256 为
`18e13bb65bf2ca4681eed6138b824ea09d79b5fdab4da3d66a86802466d06cb2`；build/contract/image-inspect
logs SHA-256 分别为
`7489b3d05eba2628597241580c69b74e1622ccdd2d33c8ce7acde0ac70ba9497`、
`6490172f26faa2b16146a927edc59d384068f374eb9e910cbc9be3680fe43f7a`、
`d8d38afc52740e1bee9bf64cc24618fb06d043e1259811be9448305a78982136`。

R114/R115 只达到 v0.11.2 complete clean-builder matrix 与 builder-local image contract，尚未
commit/push/tag，也没有 registry immutable digest、Compose、deployment 或 live evidence。追加本节
只修改 non-executable plan ledger；提交前仍需证明本节前后的非账本 source 和 release binary
byte-identical，再进行最终 Git 审计。

### 13.63 R116 v0.11.2 最终 ledger-only 等价证明 green

R114/R115 前的 exact-source archive 为
`e0c71374f3f8c2707031f01689f8f648d05e972de8c4d64bcea86fc9363e0dec`；追加两节证据账本后的
commit candidate archive 为
`7cfc3b0599d4b56961f18b86b8bd8db169160c18556ddadf9efd61de2ccbbe50`、Git index tree
`70f70cf6f4fbfeae854c245a5786222a7b3b0dce`。本地预审和授权 builder 均分别解压两份 archive；
排除唯一 canonical plan 后各有 296 个文件，path/content manifest byte-identical，两个 manifest
SHA-256 均为 `d1396568ca1c4674aa54e196c760c2c8f40fdc755a9e20d4f85a65373eef2873`。
账本自身的 SHA-256 分别为
`dfeaae5f9b61fcdee32a6caaa5c8dea40a02cba1b4de235c99a64ef1fd02046b` 和
`bd0dc5d4511d8d6faa365df16e3d7f4c2b08cfe6a0969230addf22af34323ec5`。

在两份 source 上分别执行 `go build -trimpath -buildvcs=false`，两个 11,744,728-byte binary
SHA-256 都是 `478bea65988e7cedfd4cb03d3e1696d4fbc4d0b3b663c10fb726f0764cf35c33`，并通过 `cmp`
byte-identical。持久化 evidence 位于 builder
`/work/pig-v0112-r116-ledger-equivalence-20260806`；最终 `run.log` SHA-256 为
`13b662b576469512a588a4a103443437ce737a502b0ca914e957b92a59964247`，binary hash 文件 SHA-256 为
`b3fbcb60fb9a453e9f5571c2e77490ae52f98be17ae219f066738f7db66042f3`。

因此 R114 exact-source matrix 和 R115 builder-local image contract 可严格继承到最终 executable
candidate。本节自身仍只是 non-executable ledger append；最终 Git 审计必须确认此后没有任何
非账本 source 变化，并继续排除两个历史 untracked plans。当前仍没有 v0.11.2 commit/push/tag、
registry digest、Compose、deployment 或 live evidence。

### 13.64 R117-R119 v0.11.2 clean tag 与 immutable registry image green

最终 Git 审计确认 R116 后没有非账本 source 变化，两个历史 untracked plans 仍未 stage。corrective
commit、branch push 和 annotated tag 均完成：

```text
source commit:
  533106b869c15b9199124c3fb2fbd5d2c1a78dc8
source tree:
  14f488d98d1ee8b9922da5a35ad2c962888c3593
branch:
  pig-origin/codex/pig-v0.11.0-request-aware
annotated tag object:
  70107aea3f23ff99e451abe1535a8749afc07080
tag dereference:
  v0.11.2^{} -> 533106b869c15b9199124c3fb2fbd5d2c1a78dc8
```

builder host 不含 `git`，R117 首次 clean-clone 脚本在任何 clone/image build 前停止；没有生成 image
或覆盖已有路径。R118 改为由同一授权 Go container clone 公开 `v0.11.2` tag 到持久化 `/work`，
验证 detached HEAD 精确等于上述 commit、tree 正确、exact tag 为 `v0.11.2`、porcelain status
为 0 行；host Docker 再从这一份 source 构建。clean-tag builder image ID 与 R115 candidate 一致：
`sha256:2323d54212de804dd6964b0c6236f3ffa10002b87d16d3343c7a68fa933ca085`；
production-image contract 再次通过，提取 binary 与 R115 candidate byte-identical，SHA-256 均为
`18e13bb65bf2ca4681eed6138b824ea09d79b5fdab4da3d66a86802466d06cb2`。R118 clone/source/build/
contract/image-inspect/binary-hash logs SHA-256 分别为
`1f9cd2b08257534d7ae7aa1d412c11f4df2efc1aedf2fa8795410641b0137ac6`、
`1741bc4c330fe23eef1dc7325f18f35318f5d21a37f5f3e8bb23a64090c75c89`、
`6b99f385dd75a8bfd9b92412929f896cf717c45fb3253c6bb708ac3236e548d0`、
`9556d7a3d2ec35a9de37064bdb48802b1273b19e38b531280b548236c61544d7`、
`799fa6b1b41f72649968ec0f11b1b7463c8f0a258b86a002d63d368b3a676d19`、
`8d0f8407428aea78ebcdea91ab54b1029adb52fb7b6198de634758f3ca6e40a2`。

GitHub `Publish Image` run `31079638436` 对 `v0.11.2` 成功完成 contract 与 build-push。R119 从
registry tag pull 后锁定 immutable image：

```text
ghcr.io/phala-network/phala-inference-guard@sha256:30d99d9e19a7640a40b093aca5ab7727c67c57eba8fbf97d8d23a00e0090a7d1
```

按 digest 再次 pull、执行 `EXPECTED_VERSION=v0.11.2` production-image contract 均通过；pulled
registry image ID 为
`sha256:86f8a9ea12b52a2805d53eb511cbe0c16a0dc09e8942acb164ba44aedae5ac55`。registry
binary 与 clean-tag builder binary byte-identical，SHA-256 同为
`18e13bb65bf2ca4681eed6138b824ea09d79b5fdab4da3d66a86802466d06cb2`。R119 tag-pull/
digest-pull/contract/image-inspect/binary-hash logs SHA-256 分别为
`a893931dbb58e0d8dac0fdf10ee04e5d26153656ee45fb7810b2e5c5ef303e5c`、
`0b07f02ec8c2423677590ecdd26ba8a82bd543040f8627a19229f94b4533ade3`、
`3cf3c74a20063c2d0edea5fbf9e43fbacb79ba0d2e28f332906c8385823ca354`、
`f900b24c88eff11594af0882fb865c0e4051d11f605cbdf4087925e0d6df43f6`、
`9087c12b2ea385aa538c29eded6359883569f6c3ae1fa94115c3a9ad91decb95`。

R119 已达到 source tag、builder-local image、registry publication、immutable digest 与 binary
provenance 层；尚未修改 `use1-cb` Compose、CVM、Router 或生产流量。下一步必须重新查询当前 live
Compose/hash、Router enabled set 和节点 readiness，再仅以 Router disabled shadow 部署该 digest；
不得由 registry green 直接 enable Router。

### 13.65 R120 v0.11.2 Router-disabled shadow 与分层尺度错误

2026-08-06 实时 authenticated preflight 确认 `use1-cb=false`，Router enabled set 仍为
`use1-9b,use1-19`，Router config digest 为
`sha256:b8447a719c6fb9d8bf956ae60928d5e857828e7c2874739e7bb8fae8cca5c47a`；目标 route
`running=0`，直连 PIG/vLLM running、waiting、KV、preemption 和 reservation 均为 0。live Docker
Compose UTF-8 SHA-256 为
`d6458ac3baf8d46f59a3ad6789708b63c2fa4602fc6ef96ab4e09509c763192e`。从该 snapshot 生成 rollback、
v0.11.2 shadow 和 enforce candidates；normalized diff 只包含 PIG immutable digest、shadow mode 和
四个显式 Prefill defaults。shadow/enforce candidate SHA-256 分别为
`0ab72ecc4e878de900466d2e320385d6357694c676b797253b69503501bbe89d`、
`264fd63469defd4779333923807e315bb8fe30b781a33b82b41a161c7c6763aa`，candidate summary SHA-256 为
`d1cfc4ab96a9d28b7ca5b4560916d6ceab2e7c63b6641552ff4bd018026bb541`。

predeploy drift check 再次得到相同 live Compose SHA-256、Router digest、enabled set、
`use1-cb=false` 和 route `running=0`。随后只部署上述 shadow candidate，不传 `.env`，deploy 从
`2026-08-06T07:33:06Z` 到 `07:37:30Z`、exit 0；deploy summary SHA-256 为
`c5c94e1f1b97be0896d04f32f2a6d47caa9f99203231adbd420121bf958ced62`。更新后的平台 Compose hash 为
`0cc84bd448797bf00f9d607e6106fa47471b151c0eca42447a264eff7ecf00b4`，Docker Compose SHA-256 精确等于
shadow candidate；PIG image digest/image ID 分别为 R119 immutable digest 和
`sha256:86f8a9ea12b52a2805d53eb511cbe0c16a0dc09e8942acb164ba44aedae5ac55`。

readiness 期间 `/v1/models` 从 503/偶发 connect timeout 恢复为 200；最终 PIG/vLLM metrics 和
attestation authenticated 200，未认证 metrics 401，NVIDIA payload 非空，endpoint status evidence
SHA-256 为 `6c9813d4fba493817abdd4d044c6d032cd5500c160349650b036b416caeb14e4`。
runtime metrics 明确为 `PIG-v0.11.2`、`shadow`、intake open、reservation/pending Prefill/Router
backpressure 均为 0；vLLM 启动确认 max-model-len 262,144、KV capacity 862,437 tokens、模型加载、
CUDA graph capture 和 server start，当前 boot 未出现 CUDA OOM、NVRM Xid、EngineDead、host OOM 或
panic。vLLM/serial evidence SHA-256 分别为
`dc18a56dbf2916ba150479ed13f94d56e571fb33b6be16129a314cb49e0291df`、
`e88c8820e1fe9a55505ca5cfd069aeb268c68a312e68d7f0b924b13d35de2e7d`。

Router-disabled chat、stream、required tool、strict JSON Schema、Responses API 和 CJK 六项均为 200，
protocol summary SHA-256 为
`b129aef9ebfd6d61acc52e1d07e41a7acdaa757ae7ff8b45e6fb68bb89a21d65`；请求后 PIG attempts=6、fit=6、
enforced rejects=0、reservation/pending/backpressure/failures=0，vLLM success=6 且再次 idle。低流、
completion-window、mid-stream cancel、post-cancel recovery 和 12 并发 burst 首轮只因测试器错误要求
取消后的 HTTP status 必须为 `000` 而 red；实际已收到 200 response headers 和 6,004 bytes 后 curl
按 2 秒退出 28。修正该判据后，r2 唯一失败是一个请求在 connect 阶段 20 秒 `HTTP 000`，PIG
attempt delta 也精确少一，证明未进入 PIG；普通请求增加最多两次 transport-only retry、取消不重试的
r3 全绿。三份 summary SHA-256 依次为
`29e84fbed1bfc39bd4979de5686db7e356feb659cfc895d26dc52c87d75a6561`、
`0fec0c6a0a046905a5d94b23929d73fecc0eaed27492229e54e4ceff7b96b95e`、
`00b3e7761592ae2e1679d78e80e9ba604aacb0f28791dd2f36a60db3d21ac603`。r3 结束时
running/waiting/KV/reservation/pending/preemption/backpressure/failures 全为 0；连接抖动必须作为
ingress/transport 观察项保留，不能伪装成 PIG admission 结果。

随后执行当前节点范围内的 token-size shadow probe，summary SHA-256 为
`fe3e56a7782c081b5392ec985022ab9f9e98d98d7e03295db0130e6c146e7778`：

- 80,013 actual prompt tokens、480,121-byte request 成功，但 `selection_input_tokens=99,389`、
  `estimated_prefill_tokens=240,069`，分类为 `weighted`；
- 230,013 actual prompt tokens、1,380,121-byte request 成功，但
  `selection_input_tokens=285,718`、`estimated_prefill_tokens=690,069`，分类为 `quiescent`；
- 两者结束后均无 reservation/pending/preemption/Router clamp；没有向 262K 节点发送 512K 或 650K
  actual prompt。

源码因果追踪确认 3 倍提前触发来自信号复用：`selectionInputTokens` 已是 bounded lexical hint 的
模型无关点估计；`EstimatedInputHigh` 是用于 hard KV safety 的保守输入上界。adapter 把后者写入
`RequestCost.UncachedPrefillUpper`，Manager 又把同一 upper 同时用于 Prefill class、aggregate budget
和 pending long/quiescent count。64K/256K/512K/650K 原建议针对 `estimated_uncached_tokens` 或近似
实际 Prefill 工作量，不是 JSON byte-derived safety upper。继续用 upper 会让当前 live shape 在约
实际 170K 时就进入 512K+ quiescent，明显过度保护总吞吐。因此 v0.11.2 **停止 promotion**：保持
Router disabled + shadow，不部署已生成的 enforce candidate，也不 enable `use1-cb`。

### 13.66 v0.11.3 safety upper 与 Prefill interference estimate 分离计划

corrective release 自主管理为 `v0.11.3`，不改变通用
`65,536 / 262,144 / 524,288 / 262,144` 默认值，也不增加模型名、tokenizer asset、cache、Router、
learner、TTFT、tier 或新配置。修复只分离两个已有信号：

1. `EstimatedInputHigh`/`UncachedPrefillUpper` 继续用于 hard KV fit、block-aligned reservation 和通用
   safety telemetry；它仍不是精确 cache-cold tokens；
2. bounded lexical `selectionInputTokens` 用作长 Prefill interference 的
   `EstimatedPrefillTokens`、64K/256K/512K class 和 256K aggregate pending budget；缺失或 invalid
   时仍 fail closed/回退 safety upper；
3. request-aware reservation 必须原子保存该 interference estimate；pending estimate、long count、
   quiescent count 从唯一 reservation registry 同一次扫描派生，不增加第二套可泄漏 counter；
4. generic `ForwardedPendingPrefillTokens` 可继续表达 safety upper；request-aware
   `pending_prefill_tokens` 明确表达 interference estimate，日志/metrics/计划不得混淆；
5. hard KV 决策必须在 point estimate 很小但 safety upper 超限时仍保护；信号分离不能牺牲 KV QoS。

先增加能 compile 且因 v0.11.2 行为失败的 behavioral red：

- safety upper 690K、interference estimate 285K 时 class 必须是 `exclusive` 而不是 `quiescent`；
- safety upper 240K、estimate 99K 的两个 pending request 应按 198K aggregate admit，第三个按 297K
  触发 budget；不能用 480K safety upper 错拒第二个；
- 690K safety upper 仍参与 hard KV post-admit fit，不能因 class 使用 285K 而放松；
- divergent estimate 的 reservation 在 prefill complete、terminal、cancel、rebase、Close 和 race
  下 exact-once 释放；small request 不得因 exclusive request 的 safety upper 超过 512K 而被错误
  当成 quiescent 独占保护；
- HTTP integration 必须证明相同 backend snapshot、相同 safety upper、只改变 interference estimate
  会改变 pre-forward class/verdict/telemetry；source fallback 和 invalid case 保守。

最小实现后只在授权 builder 运行 focused/full/race/vet/build、两次 deterministic simulation 和
hot-path benchmark；本地 Windows 不运行 Go executable gates。所有 executable source 变化都使
v0.11.2 的 matrix/image evidence 失效。通过三轮复查后 bump runtime/README/OCI identity、发布新的
annotated tag 和 immutable digest，再以当前 Router-disabled shadow Compose 只替换 PIG digest，重做
readiness、协议、低流和 size probe。v0.11.3 live acceptance 要求：80K case 仍为 weighted；230K
case 应以约 285K interference estimate 落入 exclusive、不能再以 690K safety upper 落入 quiescent；
hard KV safety upper 和无 preemption/residue 证据同时保留。当前 262K 节点仍不发送 512K/650K actual
prompt；这两个边界由 corrected source、生命周期测试和 multi-card deterministic simulation 证明。

### 13.67 R121 多卡 512K/650K 合同确认与 v0.11.3 三轮复查修正

用户再次确认 512K/650K 分层对多卡大模型有效。active contract 因此继续使用四档
`<64K / 64K–<256K / 256K–<512K / >=512K`；650K 是 `>=512K` quiescent 档的代表性压力用例，
不是需要第五个阈值的新档。当前 262K `use1-cb` 只限制 live actual-prompt 验证范围，不能删除 exact
512K boundary、650K idle/busy/cancel/recovery 或多卡 simulation。

本轮按 source archive 之前的三轮 review 执行，并在 review 中继续修改源码，因此 R120 focused green
不能外推到当前工作树：

1. **模型与因果。** hard KV fit、block-aligned reservation 和 generic pending telemetry 继续使用
   `EstimatedInputHigh`/`UncachedPrefillUpper`；Prefill class、aggregate interference budget 和本地
   long/quiescent count 使用 bounded lexical estimate。没有增加模型名、tokenizer asset、cache、Router、
   learner 或第五个 650K class。HTTP behavioral test 使用两个精确相同 body-byte safety envelope：低 lexical
   work 必须 regular/admit，高 lexical work 必须 >=512K quiescent/pre-forward protect；两者 reserved safety
   tokens 必须相同。另一个 hard-KV case 证明低 estimate 不会放松 safety upper。
2. **安全、生命周期与 SOLID。** interference estimate 只作为唯一 Manager reservation 的一个原子字段，
   pending estimate、long count、quiescent count 仍由同一次 registry scan 派生，没有第二套 lifecycle
   counter。新增 divergent estimate 的 Prefill-complete、terminal、client cancel、rebase race 和 adapter
   Close-before-forward 测试。`Close` 的真实合同是先关闭 intake、禁止旧句柄再 commit forward，再由 terminal
   路径 exact-once 回收；不得为迎合测试直接清空仍可能在执行的 reservation。Prefill complete 只释放
   interference budget，不能提前释放 KV ownership。
3. **evidence 与 release。** review 发现初版修复把 `PendingPrefillSequences` 取自 observed base 加本地
   reservation，却只从本地 reservation 累加 pending interference tokens；存在 observed pending sequence
   非零而 tokens 为零的 invalid/误锁缺口。现改为：无法归属 lexical estimate 的 observed pending work 从
   observed `UncachedPrefillTokens` safety upper 保守起算，本地 reservation 再按 interference estimate
   累加；缺失的 reservation metadata 同样回退自己的 safety upper。新增两项因果测试，防止该缺口回归。

deterministic simulation 当前新增：

- live-shaped safety upper 240K / estimate 99K：两个请求按 198K admit，第三个按 297K budget protect；
- live-shaped safety upper 690K / estimate 285K：在已有 Decode 时允许一个 exclusive 和一个 short，第二个
  exclusive pre-forward protect，不能因 690K safety upper 把首个请求误判成 quiescent；
- exact 512K busy boundary：必须进入 quiescent protection；
- 既有 idle/busy/cancel/recovery 650K 场景保持不变。

截至本节只完成 source/test/simulation 修改与本地 `git diff --check`；未在本地 Windows 运行 Go executable。
下一步必须从当前 exact source 重新封存 archive，并仅在授权 builder 执行 whole-repo gofmt、focused、full、
race、vet、build、两次 byte-identical simulation 和 benchmark。当前仍不得 bump/tag/publish/deploy v0.11.3，
不得部署 v0.11.2 enforce candidate，`use1-cb` 必须继续 Router disabled + v0.11.2 shadow。

### 13.68 R121/R122 性能复查与 R123 单次扫描优化

R121 exact candidate archive SHA-256 为
`c3eb78c50c0575bc8d2db83fa563f08acdc4ebac17d35e83dd9fa4efae41af05`；focused evidence archive
SHA-256 为 `6ebabcb6096fa4c3b09637440f3f91c99f8da5d3a6999a3886f2f7262a4c7b37`。R121 full evidence
archive SHA-256 为 `a4341b7b83f23c4e6b4bdaff204ac97a866f114500c26284c72081bfd90b5017`，所有
test/vet/build/race/simulation/acceptance gate 均为 0。两次 deterministic simulation byte-identical，report
SHA-256 为 `3e4e29c32b7dfed28326d916f01d39e642b2edabc492358b43d354ce2777208f`；aggregate
completion TPS 为 `50.3645 -> 68.2720`，SLO completion TPS 为 `46.2056 -> 67.5911`，preemption
保持 `1 -> 1`，TPS-floor violation 为 `89.7s -> 7.5s`。这些只属于 deterministic model evidence，
不是实际 GPU throughput 证据。

R121 没有直接接受为 performance green：相对 exact v0.11.2 baseline，Manager 48/256 active reservation
约慢 `12.7%/12.9%`，虽然绝对增加仅约 `0.62us/3.3us` 且保持 0 allocation。R122 尝试把 lexical
interference estimate 临时写入 map-value copy 的 `Cost.UncachedPrefillUpper` 再复用 interval helper，archive
SHA-256 `6554a732586ae5531ac6c9d0f12ae428c652794d5b906312035f1a07b29c5567`、focused evidence
SHA-256 `280b629829311026a6258b8eb1912ed4e203716313d1e629dfb9944673fa23d7`；功能和 race 仍通过，
但 48/256 active regression 恶化到约 `14.8%/14.9%`，因此明确拒绝 R122，并恢复显式、独立的
`pendingPrefillTokens` saturating sum。`RequestCost` 的 safety invariant 没有被修改。

源码复查定位到真正的额外成本：`reservation` 同时包含 `RequestCost`、完整 scheduler prediction 和 lifecycle
metadata；map range 已产生一个局部 value，而 `addReservationToStateInterval` 又按值接收整个 reservation。
R123 只把该 helper 及两个 cost projection helper 改为读取局部 value 的指针，仍然：

- 扫描唯一 `reservations` registry 一次，不增加 aggregate counter、第二张 map 或 pointer-valued ownership；
- generic virtual KV/state 继续使用原始 safety upper；request-aware pending interference 继续独立使用 lexical
  estimate，missing metadata 和 observed base 继续回退 safety upper；
- helper 不修改 reservation；map ownership、锁、terminal、cancel、prefill-complete、rebase 和 Close 生命周期
  均不变；
- exact 512K boundary 和 650K idle/busy/cancel/recovery 仍属于同一个 `>=512K` quiescent 档。

R123 candidate archive SHA-256 为
`339431da8acd23e782b8fed412d7acb9518f092021a11c8605e708e0c067ce38`，只包含 297 个 tracked
files；两个历史 untracked plans 未进入 archive。授权 builder 仍为 `cvm_3e2k83KX` / `pig-v01011-builder` /
Go `1.24.13 linux/amd64`，container `running=true`、`OOMKilled=false`、restart count `0`。CVM 重启后的
ED25519 host key 使用独立 known-hosts 锁定并复核为既有指纹
`SHA256:cL6Yhk0milH+/UJcYwy9ebox+uT6HJfMkAKo26pZO3M`，没有关闭 host-key 校验。

R123 focused evidence archive SHA-256 为
`5423bb5c499e453c5a04203dc08006f5bf1ae5e75d19fc8e82ac0d7e06e36cfb`：whole-repo gofmt
为空，focused tests/race 和 required simulation acceptance 均通过。Go compiler optimization log SHA-256
`09d41fda40e8c3e4aa2ac98cd0b6cd5744fa198966552649d7d2300073e2ca8f` 明确报告 local
`item does not escape`，helper 参数不 escape；Manager benchmark 仍为 `0 B/op / 0 allocs/op`。

R123 full evidence archive SHA-256 为
`8b317c58914468d612ded6ac111463fcf016cbb7ebd223cf3fa36190b983d548`。以下 gate 的 exit 均为
0：`go test ./...`、`go vet ./...`、`go build ./...`、`go test -race ./...`、两次 simulation、byte replay、
acceptance、policy/Manager/HTTP benchmark。full-test、full-race 和 executable source manifest log SHA-256
分别为 `f3fb13e2780c338568499a29684620999b5dfcca26358c73f466699b1089cae6`、
`cbdfaa9ebf7296f1c923f8dd9acb1a7c8127f1b66ab2b7f4ed502e9a9c537886`、
`7b2973f040f8ab189b2c37300ab353668d630d470bc9eb497111b4ae2cbedcbd`。两次 51,772-byte
simulation 仍 byte-identical，SHA-256 与 R121 相同，证明该性能重构没有改变任何 deterministic verdict 或
aggregate result。

交错 500ms、每档 10 个样本的 median 如下；全部为 0 allocation：

| Manager active reservations | v0.11.2 baseline | R123 | delta |
|---:|---:|---:|---:|
| 0 | 163.4ns | 149.3ns | -8.63% |
| 48 | 4,869.5ns | 4,544ns | -6.68% |
| 256 | 25,705.5ns | 24,039.5ns | -6.48% |

HTTP pre-forward fixture median 为 `36,317ns -> 36,074ns`，双方均为 `39,290 B/op / 119 allocs/op`。
因此 R123 接受为当前 performance green：它消除了 R121/R122 的相对回退，且没有用语义 hack、第二套
lifecycle state 或 allocation 换取速度。

### 13.69 R123 三轮 final review 与当前 promotion 边界

1. **模型与因果。** 四档仍为 `<64K / 64K–<256K / 256K–<512K / >=512K`；650K 是最后一档
   的多卡代表用例，不是第五档。hard KV/block-aligned reservation 使用 safety upper，Prefill interference
   class/budget 使用 bounded lexical estimate。相同 body safety envelope 的 HTTP 因果测试、690K upper/
   285K estimate hard-KV 与 exclusive 测试均通过；没有模型名、tokenizer asset、cache、Router 或 learner 分支。
2. **安全、生命周期与 SOLID。** Manager 仍是唯一 reservation owner；state projection helper 只负责把
   reservation 投影到 virtual state，request-aware manager 只负责组合 observed base、local interference 和
   policy input。传入局部 copy 指针消除重复大对象复制，但不把 map 改为 pointer map；编译器和 benchmark 共同
   证明不 escape、0 allocation。terminal/cancel/rebase/Close/race、observed fallback 和 exact-once release 测试
   全部保持 green。
3. **evidence 与 release。** R123 exact tracked-file archive 已完成 focused 和 full clean-builder matrix；
   simulation 只证明确定性模型合同，不冒充真实 GPU goodput。当前完成层级为 source implementation + focused
   tests/race + complete builder matrix；尚未 bump v0.11.3 runtime/README/OCI identity，尚未 commit/push/tag，
   尚未构建或发布 image，也未修改 Compose、部署 CVM、发送 live probe 或 enable Router。

下一步先做 v0.11.3 identity-only bump，并证明除 version/ledger 外 executable source 与 R123 等价；随后
commit/push、annotated tag、从 clean tag 构建 immutable image及验证 source/tag/tree/binary/image provenance。
只有完成这些 release gates 后，才重新读取 live Router/CVM/Compose，并在 `use1-cb` Router-disabled 条件下仅部署
v0.11.3 shadow。当前仍禁止 v0.11.2 enforce promotion、Router enable 和向 262K 节点发送 512K/650K actual
prompt；512K/650K 的通用多卡合同继续由 source、lifecycle tests 和 deterministic multi-card simulation 保证。

### 13.70 R124 v0.11.3 release identity 与完整 builder matrix

R123 final review 之后只执行 release identity 和说明收口：runtime status constant 改为 `PIG-v0.11.3`，
Dockerfile OCI version label 改为 `0.11.3`，README 标题和行为说明改为 v0.11.3，并明确 hard KV 使用
safety upper、Prefill class/budget 使用 lexical estimate、缺失时才回退 safety upper。64K/256K/512K/650K
算法、默认阈值、reservation lifecycle、policy 和 simulation scenario 均未改变。

12 个 task-owned tracked paths 明确 stage；两个历史 untracked plans 仍未 stage。staged index tree 为
`72cc7a8795f9d629383b954976d6332213b791b4`，从该 tree 直接生成的 297-file release archive SHA-256
为 `949d0020d32691f2c3686172db900147a12be36ec192a151f6434a28c932d64e`。授权 builder/container/
Go 环境与 R123 相同且 `running=true`、`OOMKilled=false`、restart count `0`。

R124 release evidence archive SHA-256 为
`7e9b352964a1518e02bb9e2f1b3b36cc180229e2246fa025259092e6c6c71ea7`；为避免通过慢速 SSH
重复传输 12MB binary，另生成排除 binary 本体、但保留 binary hash/version log 和全部 gate log 的 slim evidence
archive，SHA-256 为 `4eb0f3dacca705cb9809cd6eaa13d3086b42905f1d1a530b60f9a924fe919d53`。
两者都来自同一个 builder evidence 目录，slim archive 不作为新的执行结果。

所有 release gate exit 均为 0：whole-repo gofmt 为空，`go test ./...`、`go vet ./...`、`go build ./...`、
`go test -race ./...`、独立 release binary build、两次 simulation、byte replay、acceptance 和全部 benchmark
通过。full-test、full-race 和 executable source manifest log SHA-256 分别为
`504b3289c9d78e0292eb8e91b40b31456fe3d049b8658755621bfec14fe18e46`、
`a42ff7b06659a9b6a547ba26b26ff4cb7ad460ca75696c9f8c616ecc316cf118`、
`ed20bb77129ba1f7144cfd0a55611523224926f6bd0b7efa6f4936522777d419`。release binary SHA-256
为 `d5f8b709e65f1e316a5dd20a9a2a3a7eaa280e33f674aea8bad9d2a832d88b7e`，strings gate 确认包含
`PIG-v0.11.3`。该 binary 仅是 release identity 证据，不作为待部署 registry image。

两次 51,772-byte simulation 再次 byte-identical，SHA-256 仍为
`3e4e29c32b7dfed28326d916f01d39e642b2edabc492358b43d354ce2777208f`、acceptance=`passed`，
因此 identity bump 没有改变 exact 512K boundary、650K idle/busy/cancel/recovery、690K upper/285K estimate
或 aggregate deterministic result。R124 交错 benchmark median（全部 0 allocation）为：Manager active
0/48/256 从 `163.75ns / 4,871ns / 25,712.5ns` 变为
`149.3ns / 4,538.5ns / 24,015ns`，即 `-8.82% / -6.83% / -6.60%`；HTTP median 从
`36,342ns` 变为 `36,265ns`，两边仍为 `39,290 B/op / 119 allocs/op`。

截至本节，v0.11.3 已达到 staged source + exact release archive + complete clean-builder matrix；仍未 commit、
push、tag、build/publish registry image、修改 Compose、部署或 enable Router。本节只是 canonical ledger 追加，
下一步必须证明相对 tested index tree 唯一变化为本计划文档，再创建 source commit 和 annotated tag；image 必须
从 clean pushed tag 构建并重新验证 OCI label、binary status、tag/tree/image digest 对应关系。

### 13.71 R125 clean tag 与 immutable registry image green

v0.11.3 source release 已完成：

```text
source commit:
  0b4da6bf22bc80655eabf977e278eedc34033dad
source tree:
  33e15126def41beb427b22caa945db965925fb55
branch:
  pig-origin/codex/pig-v0.11.0-request-aware
annotated tag object:
  77e93b4dc5049ff8880b4afa8058019b3e51213e
tag dereference:
  v0.11.3^{} -> 0b4da6bf22bc80655eabf977e278eedc34033dad
```

GitHub `Publish Image` run `31090558312` 对 `v0.11.3`、上述 exact head commit 成功完成 contract 和
build-push。授权 builder 随后由 `pig-v01011-builder` clean clone public annotated tag；source identity 为
head `0b4da6b...`、tree `33e1512...`、exact tag `v0.11.3`、porcelain status 0 行。host Docker 从该
clean source 构建 local image，并从 registry tag pull 后锁定 immutable digest：

```text
ghcr.io/phala-network/phala-inference-guard@sha256:15d827456c56a534d71b03932d5a9a90d2d7984e5cbfec6aec3b2632cfcc0d99
```

local clean-tag image 和 registry digest image 均为 linux/amd64、OCI label `0.11.3`、29,258,447 bytes、
distroless entrypoint `/phala-inference-guard`，并分别通过
`EXPECTED_VERSION=v0.11.3 tools/validate-production-image-contract.sh`。local image ID 为
`sha256:267fec1c36d147e58b4f6fea993a66965bf01349a814376829098233c8d689bb`，registry image ID 为
`sha256:a5f1f711ef0aa66d5ba3d58064c429035b77e1a915cab3389f7ecadcd65128a3`；image config identity 可不同，
但二者提取的 runtime binary byte-identical，SHA-256 均为
`3fdb3e3240854c120740f4fbec82155c174015a43490997771a1cf15e313262f`、`cmp=0`。local 和 registry
image 的无流量 startup 均保持 `running=true`、exit code 0，日志包含 `PIG-v0.11.3`。

首个 image harness 已完成 clone/build/contracts/pull/binary cmp，随后因 BusyBox `test` precondition 写法收到
空整数而退出；resume 已完成两个 startup gate，又因 BusyBox `xargs` 不支持 `-0` 在 evidence sealing 处退出。
两项均为 harness-only、发生在对应 product gate 成功之后。最终使用 POSIX `for` 仅封存既有证据，不重跑或
掩盖任何 product gate。slim evidence archive SHA-256 为
`57b86f71005d901d9677ed400502152bd36481bf45bce7017affbb056e001446`；clean source identity、local/
registry contract、binary hash、startup logs/state 和 image inspect 均在 archive 的 `SHA256SUMS` 中。

当前完成层级为 committed/pushed source、annotated tag、successful registry workflow、immutable registry
digest、clean-tag/local-to-registry binary provenance 和 image startup；尚未修改 `use1-cb` Compose、部署 CVM、
发送 live probe 或 enable Router。下一步必须切换到 live-serving 运维流程，重新查询 CVM、exact Compose、当前
Router inventory/enabled 状态和 v0.11.2 baseline；只有确认 `use1-cb` 仍 Router disabled，才可仅把 PIG digest
替换为上述 v0.11.3 digest并保持 shadow。image green 不能直接外推为部署或实际流量 green。

### 13.72 R126 v0.11.3 Router-disabled shadow live green

2026-08-06 live preflight 与 deploy drift check 均确认目标
`a0f0bfb3-e46f-4b22-814e-24872f251193`（`gemma4-31b-it-use1-cb`）仍为
`use1-cb=false`，Router enabled set 为 `use1-9b,use1-19`、config digest 为
`sha256:b8447a719c6fb9d8bf956ae60928d5e857828e7c2874739e7bb8fae8cca5c47a`。shadow candidate
只把 PIG image 从 v0.11.2 immutable digest替换为 R125 v0.11.3 digest；vLLM、HAProxy、ingress、
`PREDICTIVE_ADMISSION_MODE=shadow`、`DYNAMIC_TTFT_ENABLED=false`、500-ms predictive observation poll、
1500-ms maximum metrics age 和四档 Prefill 合同均未改变。candidate Compose SHA-256 为
`ad22fb658bb72ad01f17ccde00960029bf49a8a89f9e7fdfe38adc175a956b99`，rollback Compose SHA-256 为
`0ab72ecc4e878de900466d2e320385d6357694c676b797253b69503501bbe89d`。

`phala deploy --wait` 从 `2026-08-06T10:01:51.5335291Z` 到
`2026-08-06T10:06:41.3171094Z`、exit 0；deploy summary SHA-256 为
`e0e68b8251e7b46607541e157a198b730b250aa2a34ba70682e54289f3cfcdeb`。平台随后精确返回上述
Compose SHA、v0.11.3 digest 与 registry image ID
`sha256:a5f1f711ef0aa66d5ba3d58064c429035b77e1a915cab3389f7ecadcd65128a3`；vLLM image/digest 未变。

第一次 post-deploy readiness 在 `10:08:28Z` 正确失败：CVM 和容器已 running，但 authenticated
models/metrics/attestation 仍为 503，summary SHA-256 为
`97afcec83c2843765755b3070e25c31a25e8d5823f3c2ddf1e0c2421f8abeb47`。没有重复 deploy 或把
`running` 冒充 ready。current-boot logs 随后证明 vLLM 直到 `10:12:16.980748162Z` 才出现
`Application startup complete`，PIG 在 `10:12:17Z` 启动并于下一状态周期从 backend unavailable
恢复 green。完整 readiness 复采 SHA-256 为
`d078d7f5d1a839f5db3862dd874fecf66ad3268414e5ce8071ae801726b49bd5`：authenticated
`/v1/models`、PIG metrics、vLLM metrics、attestation 全部 200，unauthenticated metrics 为 401，
NVIDIA attestation 非空，runtime metric 为 `PIG-v0.11.3`、mode `shadow`、TTFT gate disabled、intake open，
running/waiting/KV/preemption/reservation 均为零。15-min current-boot fatal scan 三类均为 0，summary
SHA-256 为 `895980581f8ab4937645fe3bda6a45a5f0c7aae17774af37f5440d511e6f799c`。启动日志中的
`poll=100ms` 是 `QoSQueuePoll`，不是 predictive observation cadence；exact Compose 与 factory wiring
共同确认 observer 使用 `PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS=500`。

Router-disabled direct shadow protocol、低流/取消/burst 与输入尺度 gates 随后依次通过：

- protocol summary SHA-256
  `e1e03c456e6c97146d73281178255c4cb0ffdbe866e676c5775636061f4e6ffc`：普通 chat、stream、tool call、
  JSON Schema、`/v1/responses` 和 CJK 共 6 个请求全部 200，attempts 精确 `0 -> 6`；enforced reject、
  Router clamp、reservation 和 pending Prefill 为零；
- low-flow summary SHA-256
  `0fe81d2cf744a8f01ec4d6a0ca1e8c03b4dd470ebf0194c0ee7058ae6b0e338c`：首个低流请求未自锁，
  11 个普通/恢复请求与 12 并发短 burst 全部 200；2 秒 streaming client cancel 得到 curl exit 28 且已
  接收 body，随后 recovery 200；attempts 精确 `6 -> 30`，最终无 running/waiting/reservation/pending/
  failure/preemption/clamp 残留；
- strict v0.11.3 size summary SHA-256
  `8d27a2bdfa327a4f6a44a1937fa10e5d26a10122bb10337709ff976b04b8c9d1`：80,013 actual prompt
  tokens 得到 lexical/interference estimate `99,389`、hard safety reserved tokens `240,128`，分类
  `weighted`；230,013 actual prompt tokens 得到 estimate `285,718`、hard safety reserved tokens
  `690,112`，严格分类 `exclusive` 且不是 `quiescent`。这证明 v0.11.2 的三倍提前分层错误已在 live
  request path 修正，同时 hard-KV safety upper 没有被 lexical estimate 放松。两个请求均 200，最终无
  residue、failure、preemption 或 clamp。当前 262K 节点没有发送 512K/650K actual prompt；通用多卡
  `<64K / 64K–<256K / 256K–<512K / >=512K` 合同、exact 512K boundary 与 650K representative
  case 继续由 R124 builder tests 和 deterministic multi-card simulation 证明。

shadow 最终复采 summary SHA-256 为
`ddc6bd4c6d8a35d27bedfb89629a82d60918dbb2134119d713760391a78b4558`：CVM/progress、exact
Compose/image、全部 authenticated endpoints、401 auth boundary、attestation、idle/no-residue 与 Router disabled
保持 green；25-min current-boot fatal scan 三类仍为 0，summary SHA-256 为
`4e7b2982e3f3f3c0f76f9542ed8d4d5bbbf2a1393b50c1c62625f4e85b0a6f08`。final metrics 为
attempts `32`、fit/risk/unknown `28/4/0`、enforced rejects `0`。四个 shadow risk 来自 12 并发短 burst
期间的 TPS selective pressure，不是 long-tier 错分；rate-limited decision log明确记录
`mode=shadow enforced=false action=size_protect pressure_source=tps`。因此 enforce 验收不能错误要求压力 burst
全部 200：允许选择性 429，但必须同时证明普通低流不误锁、429 无 upstream forward、保护在日志/metrics 与
Router projection 中可见、取消/terminal exact release、pressure 消失后不超过 fresh poll 自动恢复，以及无
preemption/restart/OOM。

一个 protocol harness 首次仅在 metrics precondition 阶段因 PowerShell regex 缺少多行模式退出，没有发送任何
推理请求；补上 `(?m)` 后上述 final protocol green。另一个本地 5 秒 wrapper timeout 之后原 capture 进程仍完成，
没有被当作新的 product gate。final protocol/low-flow/size harness SHA-256 分别为
`9939f2e12ca52d62558219153af5d46537bbabaac7b27b5537ba5b65a4df213c`、
`dd7c7928ddc1ba972e18dcc41c0e8f776c1f1fcccf00289803d0cf120bac1139`、
`1e7fbd96fdf05f16cbc5a337f933549d8d3fdce504e7727742a5c56c0c3ce30d`；所有 summary
均不保留 request/response body，artifact secret scan clean。

当前完成层级为 v0.11.3 source/tag/image + Router-disabled shadow deployment/readiness/protocol/low-flow/
cancel/burst/live-sized classification。`use1-cb` 仍保持 Router disabled + shadow；尚未部署 enforce、尚未证明
enforce 429/Router projection/recovery，也未 enable Router 或开始 30 分钟实际流量观察。下一步只能从当前 live
Compose 生成单行 `shadow -> enforce` candidate，重新执行 drift check、deploy、readiness、enforce-specific gates；
这些全绿后才允许 Router enable。

### 13.73 R127 v0.11.3 Router-disabled enforce、三轮 final review 与多卡合同确认

R126 之后已按预注册边界只把 `PREDICTIVE_ADMISSION_MODE=shadow` 改为 `enforce`，当前 live
Compose SHA-256 为
`0c6debae711a56c45117f4d3f951e2ab0cdd58be7630721d8bdea21a5f3a6775`；可回退 shadow Compose
SHA-256 为 `ad22fb658bb72ad01f17ccde00960029bf49a8a89f9e7fdfe38adc175a956b99`。PIG image 仍精确为
`ghcr.io/phala-network/phala-inference-guard@sha256:15d827456c56a534d71b03932d5a9a90d2d7984e5cbfec6aec3b2632cfcc0d99`，
registry image ID 仍为
`sha256:a5f1f711ef0aa66d5ba3d58064c429035b77e1a915cab3389f7ecadcd65128a3`。Router 始终未参与
enforce direct gates：`use1-cb=false`，enabled set 精确为 `use1-19,use1-9b`，config digest 为
`sha256:b8447a719c6fb9d8bf956ae60928d5e857828e7c2874739e7bb8fae8cca5c47a`。

Router-disabled enforce 证据如下，所有路径均位于 root checkout 的
`tmp/pig-v0113-use1-cb-live-20260806/enforce-r1/`：

1. **64K–<256K weighted aggregate budget green。** `enforce-weighted-budget-v0113-r8/summary.json`
   SHA-256 为 `80c8a3df3a83461c2d54f3ccf5578c1f6eb556254236ff2ddb17d0024b193531`，
   `all_passed=true`。三条 cache-cold 并发请求的 actual prompt 约 67K，lexical/interference
   estimate 均约 `87,959`，hard-KV safety upper 约 `201,088`。任意且仅两条得到 200，任意且仅
   一条在 forward 前得到 429；被拒 verdict 为 `action=size_protect`、`reason=prefill_budget`、
   `pressure_source=prefill`、`prefill_class=weighted`，HTTP/last-reject reason 为
   `request_size_at_pressure`。拒绝时 pending 为 `175,918`，post-admit 为 `263,877`，超过
   `262,144` budget；与此同时 effective hard-KV 为 `485,943`，候选 post-admit hard-KV 为
   `687,031`，仍低于 `758,912` hard limit。因此因果是 Prefill aggregate budget，而不是 hard KV。
   attempts `+4`、enforced reject `+1`、upstream success `+3`，结束后 recovery 200、无 reservation/
   pending/failure/preemption 残留。
2. **weighted harness r1--r8 只修夹具，不改产品算法。** r1 的重复 prefix 形成 cache hit；r2 把
   last-decision 当 live registry；r3 同步慢抓 metrics；r4 受 PowerShell/.NET 每主机默认 2 连接限制；
   r5 第三 body 到达过晚；r6 实际触发 hard KV；r7 错误固定要求本地命名的第三条必须拒绝。r8
   改为验收并发集合中任意且仅一条 causal 429、任意且仅两条 200 后全绿。不得据这些 harness
   red 修改 policy。
3. **低流、completion window、取消和 burst green。** `enforce-lowflow-v0113-r1/summary.json`
   SHA-256 为 `d30581956994c195172d1d8717f7a0252e7d9ef803d096386aceb1a45c57191a`，
   `all_passed=true`。首个低流请求及全部普通/恢复请求为 200；2 秒 streaming cancel 为 curl exit 28，
   已接收 `14,809` bytes，随后 recovery 200；`MaxConnectionsPerServer=32` 下 12 条真实并发短请求
   全部 200。attempts 精确 `+25`，enforced reject `+0`，最终 intake open、running/waiting/KV/
   reservation/pending/Router backpressure/failure/preemption 全为零，没有低流自锁或 sticky clamp。
4. **约 230K exclusive 产品 green，原夹具数值预期无效。**
   `enforce-exclusive-v0113-r1/summary.json` SHA-256 为
   `bb246cebc3fa94051518e5a30b7eacf6f31c376fd87f07fd0afc2cfe106c01d0`。请求 body
   `1,380,154` bytes、actual prompt `230,044` tokens、cache-cold、80.620 秒、HTTP 200；verdict
   `admit/open`、class `exclusive`，lexical/interference estimate `301,897` 严格位于
   `[256K,512K)`，hard safety upper `690,112` 且 hard-fit。attempts/upstream success 各 `+1`，无
   reject/failure/preemption/residue。summary 的 `all_passed=false` 唯一来自夹具把随机 GUID nonce
   下的 lexical estimate 固定要求为 `280K--292K`；正确产品验收是 `[256K,512K)`，因此这是
   harness false negative，不是算法 failure，也不再重复一次 80 秒 Prefill。
5. **最终只读 preflight green。** `final-preflight-20260806-r2/summary.json` SHA-256 为
   `d13d2ae78daeff050054e5782e868a0e5f27ca1ff416e6540152a8426f5fd071`，采集时间
   `2026-08-06T12:14:08.4648576Z`，`all_passed=true`。CVM running/no operation/no boot error、exact
   Compose/image、PIG/vLLM containers running、Router disabled/enabled set exact；authenticated
   models/PIG metrics/vLLM metrics/attestation 全 200，unauthenticated metrics 全 401，NVIDIA
   attestation 非空；runtime 为 `PIG-v0.11.3`/enforce/TTFT disabled/intake open。attempts/fit/risk/
   unknown=`64/61/3/0`，enforced rejects=`3`，vLLM success/error=`60/0`，reservation/pending/running/
   waiting/KV/failure/preemption 均为零。PIG/vLLM fatal/OOM/EngineDead/Xid scan clean，统一保护日志
   存在。只读 collector `capture-v0113-final-preflight.ps1` SHA-256 为
   `3dc4a8ea81c6c658b14a1a343e0c93ebd1d26338d8907212ba22724606221282`；r1 使用了不存在的旧
   pending metric 且漏接 native stderr，r2 改为实际 metric 并合并 stdout/stderr 后 green，未改产品。

保护可见性与 Router 语义不能混为“最后一次 429 就全局关闭”：weighted 429 已同时出现在 HTTP、
enforced-reject counter、last decision、last reject 与统一日志中；其 last-reject telemetry 为 load scope，
但每次 metrics scrape 的无副作用 one-block inspect 仍证明小请求可进入，所以
`router_backpressure_active/applied=0`、effective waiting=0、effective global-limit 为 neutral sentinel。
这是刻意的 request-selective capacity projection，不是保护漏报。若 512K+ quiescent Prefill 正在进行，
one-block inspect 也会因 pending-quiescent gate 被保护，Router-readable capacity 才应关闭；阶段结束后
下一次 scrape 自动恢复。这样既不会让 Router 绕过真实的节点级 QoS 保护，也不会因一个大请求 429
阻断所有短请求而损害 goodput。

本轮再次按 release 要求完成三遍代码/证据复查：

1. **模型与因果。** `ApproximateInputTokenHint()` 确实进入真实 pre-forward adapter；可用时成为
   `selectionInputTokens/estimatedPrefillTokens`，缺失时回退 `EstimatedInputHigh`。policy 在
   `pressure==0 -> open` 之前执行四档 gate；同一 snapshot 下改变 lexical estimate 能改变真实
   verdict。hard KV 独立使用 safety upper，没有模型名、tokenizer asset、cache、Router 或 650K
   第五档分支。
2. **安全、生命周期与 SOLID。** Manager 是唯一 reservation owner，同一 mutex 内 check+reserve；
   forward/prefill-complete/terminal exact-once，completion/error/cancel/disconnect/timeout/forward
   failure 统一收口；snapshot reconcile 不提前复用 ambiguous capacity，same-identity epoch rebase
   清旧状态。pending long/quiescent 状态从唯一 reservation map 单次扫描派生，不维护第二套易漂移
   counter。policy、observer、manager、transport/reporting 职责分离，未发现需要 executable 修复的
   问题。
3. **证据、发布与 Router。** annotated `v0.11.3^{}` 精确为
   `0b4da6bf22bc80655eabf977e278eedc34033dad`；当前 HEAD 相对 tag 只有本 canonical ledger，
   无 executable source drift。R124 exact-source builder `go test/vet/build/race`、双次 byte-identical
   simulation、exact 512K boundary 与 650K idle/busy/cancel/recovery 均 green；registry digest/image
   provenance、shadow 与 enforce 证据分层一致。weighted/exclusive harness false starts 已逐项归因，
   没有把夹具错误冒充 product red，也没有把 deterministic simulation 冒充真实 GPU throughput。

用户再次明确确认 512K/650K 分层对多卡大模型有效。因此 active contract 固定为
`<64K regular / 64K–<256K weighted / 256K–<512K exclusive / >=512K quiescent`；650K 是最后一档
代表用例，不是第五个阈值。阈值作用于快速、模型无关的 `estimatedPrefillTokens`；hard KV 使用独立
safety upper。当前 `use1-cb --max-model-len=262144` 只允许实测约 67K weighted 和约 230K
exclusive，绝不向它发送 512K/650K actual prompt；超长合同继续由 exact-source builder tests 与
deterministic multi-card simulation 证明，待真正支持该长度的多卡节点另补 live GPU 证据。

（历史结论，已被 R128 的 canary failure 取代。）综合上述门禁，v0.11.3 当时暂时满足 Router canary
前的部署候选条件，但尚无实际生产流量证据。下一步
必须重新执行同一只读 preflight；只有 exact Compose 未漂移、Router enabled set 仍精确为
`use1-19,use1-9b`、`use1-cb=false`、节点 idle/no-residue 且 endpoint/auth/log/preemption/fatal 全绿，
才允许只把 `use1-cb.enabled` 改为 true。之后完整观察 30 分钟；任一明显 QoS、preemption、低流误锁、
sticky clamp、fatal/restart 或 goodput 问题，第一动作是 disable `use1-cb` 并保存证据，再决定是否回
shadow 或 bump patch version 重走 builder/image/shadow/enforce。

### 13.74 R128 v0.11.3 Router canary failure、disable 与证据纠正

R127 的“暂时满足 Router canary 前候选条件”只是一项当时的 promotion gate 结论，已经被本节真实
canary 结果取代。Router 在 `2026-08-06T12:40:32.3649110Z` 只把 `use1-cb` 加入 enabled set：
`use1-19,use1-9b -> use1-19,use1-9b,use1-cb`，config digest 从
`sha256:b8447a719c6fb9d8bf956ae60928d5e857828e7c2874739e7bb8fae8cca5c47a` 变为
`sha256:1ee61ece6d82d2b14c31ec2747ffb0574b8306a954f152df2c157b0699ef7df1`；除目标
`enabled` 外 route inventory 未变。发现 QoS failure 后，Router 在
`2026-08-06T13:15:26.4155613Z` 只移除 `use1-cb`，enabled set 和 digest 精确恢复到原值。
目标共 enabled 约 `34m54s`。恢复动作没有重建、改名或改写其他 route。

正式 observer 从 `2026-08-06T12:52:22.0443056Z` 到
`2026-08-06T13:15:52.6308590Z`，共 `65` 个样本、`1410.5865534s`；因 Router enabled set 已按
stop rule 回退而以 `router_enabled_set_drift` 结束，不得冒充完整 30 分钟 green。summary 与原始 samples
SHA-256 分别为
`0aa1f0c7f1fd6b405ed9bdd278343c8d5fa5b3f0516c3449cdefefc8c42b3591` 和
`f4a892ba6d9e0b002bed90cccaffbf54f9f3a967f9e14041f312dafcc95d542f`。窗口内：

- target processed `+2033`，其中 selected by cache/load 为 `+284/+1749`；
- PIG attempts/fit/risk/unknown 为 `+2163/+841/+1319/+3`，enforced rejects `+1322`，拒绝率约
  `61.1%`；
- generation/prompt/cached-prompt tokens 为
  `+1,762,184/+2,534,946/+321,664`；
- predictive failure、vLLM preemption/error/abort 均为 `0`。

窗口前约 20 分钟一度表现正常：running 最大 `52`，waiting 最大 `12` 后恢复，KV 最大
`83.10%`，低于 target/hard `84%/88%`；completion TPS mean/p10/p50/p95 为
`1343.5/331.5/1298.3/2701.3`。低流时 running 从 40 以上下降到 `5` 后 PIG 仍保持 intake open，
没有低流自锁。然而约 `13:13:33Z--13:14:53Z` 出现真实 Decode freeze：vLLM prompt 和 generation
counter 同时不变，running 固定 `52`、waiting=`0`、KV 约 `77.3%`，PIG attempts/rejects 也不再增长；
generation TPS 变为 `0`，观测单用户 TPS 最低约 `0.0012`，vLLM logger 约 80 秒没有新的周期输出。
这已经违反 QoS，不能被“零 preemption、零 fatal”覆盖。

恢复发生在 Router disable **之前**：PIG 状态在 `13:14:47Z` 已报告 generation TPS 约 `2163`，
vLLM 在 `13:14:53Z` 报告约 `1772`，而 disable 是 `13:15:26Z`。因此不能声称 disable 导致恢复；
只能确认 stop rule 随后安全移除了目标流量。冻结前可复现的关键 admission 序列为：

```text
13:12:42Z  running=30, prefill=6,  reservations=31, pending safety≈104K, lexical pending≈72K
13:12:47Z  running=28, prefill=13, reservations=40, pending prefill=28, safety≈287K, lexical≈180K, gen TPS=0
13:13:07Z  running=43, waiting=14, reservations=59, pending prefill=47, safety≈304K, lexical≈209K
13:13:12Z  running=53, waiting=0,  pending prefill=41, safety≈252K
13:13:22Z  generation TPS=0
13:13:33Z  prompt TPS=0, generation TPS=0, running=52, KV≈77.3%
```

`13:12:40Z` 同一秒出现 27 条 `Media cache enabled` connector 日志，但 vLLM 禁用了 uvicorn access
log，不能把 27 条日志直接宣称为 27 个请求，也不能从现有证据还原 request body。当前最可信且能由
源码与 behavioral red 复现的 admission 缺口是：estimator 已识别 modality marker，并在
`EstimatedInputHigh` 中加入已有的 per-marker allowance；真实 request-aware adapter 却仍只消费 URL/text
lexical hint。同时 `<64K regular` 原先完全绕过 256K aggregate pending-prefill budget，因此同一 500ms
observation 周期可以连续 reserve 大量“lexical 很小、实际 media Prefill 不小”的 regular 请求。R129 只修
这一已证实的模型无关缺口，不引入 Gemma/Gemma4 分支、tokenizer asset、cache inspection 或 learner。

post-canary 最终只读采集时间为 `2026-08-06T13:26:27.3570294Z`，summary SHA-256 为
`b0b669abbb190a19b2c93dfa5c8604addbfdbd245202070cba82f59b64eed9ff`。它确认 Router enabled set 已
恢复为 `use1-19,use1-9b`、`use1-cb=false`、route running=`0`，PIG/vLLM container 运行，authenticated
models/metrics/attestation 为 200、unauthenticated metrics 为 401，runtime 仍为
`PIG-v0.11.3`/enforce/TTFT disabled/intake open；vLLM running/waiting/KV、Manager reservations 与
forwarded pending Prefills/safety tokens 均为零，且无 PIG failure、preemption、engine/GPU fatal。
但旧 pending request-aware gauges 仍显示 `33/168940`：它们来自 `lastRequestAware` 的最后一次
decision feature snapshot，不是 live registry residue。该 summary 的 `all_passed=false` 还包含旧 collector
只识别 `prefill_budget` protection log 的 false negative；不能将它解释为后端仍繁忙。collector 随后仅做
只读纠正，SHA-256 为 `c9c6a686df98af0cbc4976750da7f6ffa21e18e683427105cc40090cd1b52acd`：
区分 current residue 与 last-decision snapshot、识别任意 enforce protection，并把 request-level
`PIL.UnidentifiedImageError` 与 engine/OOM fatal 分开。旧 `Traceback` 复算为 request traceback `6`、
invalid-image request `3`、engine fatal `0`。

结论：v0.11.3 canary 明确失败并已回退，禁止用此前 shadow/enforce、零 preemption 或自恢复来重新晋级；
在 v0.11.4 完成独立 release identity、exact-source builder/image、Router-disabled shadow/enforce 与新的
promotion review 之前，不得重新部署或 enable `use1-cb`。

### 13.75 R129 v0.11.4 regular multimodal/current telemetry corrective red/green

最终 behavioral red 基于临时 snapshot commit
`3af7502a8774c5e00c558abe773f994601b70bb8`，tracked-worktree archive SHA-256 为
`3410a9b898707b13a4c977639525a454ec8fe6f2073aa2b0f68ee7e55b6832a7`，final R4 evidence archive
SHA-256 为 `fac0544786f77dcb1ba15e636ed2eefdb30b5096f76dc91a2eb857388354c820`。两个旧 untracked
plan 未进入 archive。`FORMAT_CLEAN=1`，四项 runner 都因预期 behavioral assertion 而失败：

1. pure policy 在 pending=`258,048`、candidate=`8,192`、post-admit=`266,240` 时错误
   `admit/open`，预期 `size_protect/prefill_budget/prefill`；
2. multimodal adapter 已得到 `EstimatedInputHigh=8K`、lexical=`256`、`ModalityCount=1`，但在
   waiting/TPS pressure 下仍按 256 放行，预期按已有 8K conservative upper pre-forward protect；
3. 两个 8K reservations 后旧 telemetry 仍只报告 last-decision 的 pre-existing `1/8K`，预期 current
   `2/16K`，drain 后 current 应立即变零且 last-decision 应独立保留；
4. 28 条 background Decode、同一 100ms tick 的 40 个 regular multimodal request 中，global baseline
   和旧 request-aware 都 admit 40；预期 candidate admit 32、size-protect 8。

最小 corrective 保持原 admission transaction，不增加另一套 owner 或异步状态：

- `Cost.ApproximatePrefillTokenHint()` 对纯文本继续使用 bounded lexical estimate；已识别 modality 时使用
  既有 `EstimatedInputHigh`；lexical unknown 时仍回退该 upper。hard-KV reservation 继续独立使用
  `EstimatedInputHigh + BoundedDecodeTokens`；
- regular class 仅在 `PendingLongPrefillSequences==0` 时增加 post-admit aggregate 256K budget；精确
  256K 允许，超过才 `size_protect/prefill_budget/prefill`。已有 300K exclusive Prefill 时普通 short
  仍可进入，避免把单个长请求变成全局短请求锁；weighted/exclusive/quiescent 合同不变；
- `Manager.CurrentRequestAwarePending(policy)` 从唯一 base/reservation registry 实时派生 current sequences、
  tokens、long、quiescent。原 pending metrics（包括无前缀 post-admit gauge）改为 current state；新增显式
  `last_decision_pending_*`，status 拆成 `prefill_current` 与 `prefill_last`。Router backpressure 仍只消费
  无副作用 one-block current projection，不消费 last-decision snapshot；
- deterministic burst fixture 使用 4M capacity、28 background Decode、`maximumNoWait=512`，避免被通用
  小模型 fixture 的 queue cap 在 request-aware policy 之前错误拒绝全部请求。

focused candidate snapshot commit 为 `543bed3e883b07b53b940e0f1263a7530990a141`，archive 与 focused
evidence SHA-256 分别为
`1c35d2c31c07cec0e03336c638280f82c97d2dc1310ebba339ae57ce20000ff8` 和
`4e3ce05e942dadb29ebab839ab7d2d0ea3743031f49e0de2d3cdd2de5b109654`；format、kvadmission、policy、
server、metrics、simulation 全部 exit 0。exact full candidate snapshot commit 为
`d27657ec55f0d8eb70bf5f3bd1ae082a8b44fd7b`，archive SHA-256 为
`f40e86ce0316cd7c314dcd8463a1b7afd664a6025c00db70331258604b703d70`，full evidence archive
SHA-256 为 `293478094a63891632f1675dd69329de20a20949e0a62a11e3293d44d63835e0`。

授权 builder 为 CVM `cvm_3e2k83KX`、app
`app_89811a9add5b20427ee1fbf4dc22a33984e41959`、container `pig-v01011-builder`，Go
`1.24.13 linux/amd64`；container `running=true`、`OOMKilled=false`、restart=`0`。连接使用严格
known-hosts，ED25519 fingerprint 为 `SHA256:cL6Yhk0milH+/UJcYwy9ebox+uT6HJfMkAKo26pZO3M`；未使用生成
`StrictHostKeyChecking=no` 且触发 libuv assertion 的 CLI dry-run 命令。完整 matrix：

```text
FORMAT_CLEAN=1
TEST_EXIT=0
VET_EXIT=0
BUILD_EXIT=0
RACE_EXIT=0
SIMULATION_1_EXIT=0
SIMULATION_2_EXIT=0
SIMULATION_IDENTICAL_EXIT=0
SIMULATION_ACCEPTANCE_EXIT=0
BENCHMARK_KVADMISSION_EXIT=0
BENCHMARK_POLICY_MANAGER_EXIT=0
BENCHMARK_HTTP_EXIT=0
```

两次 simulation 均为 `53,442` bytes、SHA-256
`87a4460206e372259e780f634dd4948ed647d47746d67859b65e00bc366c97bb`、byte-identical 且
acceptance=`passed`。新增 burst 场景中 baseline 为 40 admit/0 reject、TPS-floor violation `16.4s`、
SLO completion TPS `380.3813`、peak KV `428,476`、max running `68`；candidate 为 32 admit/8
size-protect、TPS-floor violation `13.2s`、SLO completion TPS `470.0935`、peak KV `362,802`、max
running `60`。这证明 deterministic model 中候选能阻止同 tick over-admission 并改善 SLO goodput；它不等于
真实 GPU throughput、真实 multimodal cache 行为或 production readiness。

builder CPU microbenchmark 全部通过且不改变 hot-path 量级：

- `ApproximatePrefillTokenHint`：纯文本约 `7.00--7.03ns/op`，multimodal 约
  `3.72--3.74ns/op`，均 0 allocation；
- full estimator：1KiB `268--277ns`、64KiB `1.961--1.977us`、1MiB
  `27.58--28.39us`、2MiB `64.54--66.47us`，0 allocation；
- bounded lexical sampling 约 `87.4--94.0ns`，pure policy 约 `17.5--45.4ns`，均 0 allocation；
- Manager decide active 0/48/256 约 `149ns/4.54--4.57us/24.02--24.17us`，0 allocation；
- HTTP pre-forward protection fixture 约 `42.10--42.44us`、`39,323--39,324 B/op`、119 allocs。

HTTP 数值包含 `httptest`、JSON、transport 和 telemetry 全路径，不能解释为 pure policy 或端到端服务延迟；
所有 benchmark 都只是 builder CPU microbenchmark。R129 没有模型名、模型专用 tokenizer、hash-pinned asset、
cache inspection、learner、TTFT admission、tier/priority、Router/vLLM source change。通用多卡
`<64K / 64K--<256K / 256K--<512K / >=512K` 与 650K representative case 保持；当前 262K
`use1-cb` 仍禁止实发 512K/650K。

截至本节，完成层级仅为 v0.11.4 source candidate + behavioral red/focused green/full clean-builder matrix。
尚未完成三遍 final review、runtime/README/OCI release identity、commit/push/tag、image build/publish、Compose、
部署、readiness 或生产流量。R129 full matrix 不能继承 v0.11.3 image/live evidence，也不能授权重新 enable
`use1-cb`。

### 13.77 R131 v0.11.4 release identity 与 exact-source matrix

R130 三遍 final review 后只执行 release 收口：runtime constant 改为 `PIG-v0.11.4`，Dockerfile OCI label
改为 `0.11.4`，README 标题和 active behavior 改为 v0.11.4。README 同时纠正三项会误导部署者的旧说明：

- text-only 使用 bounded lexical hint；recognized multimodal 使用已有 conservative input upper，且 hard-KV
  safety upper 独立；
- `<64K regular` 在没有 pending exclusive-or-larger 时也参与 256K aggregate regular/weighted budget，已有
  exclusive 时 short 仍可继续；
- operational pending 与无前缀 post-admit metrics 是 current，只有显式 `last_decision_*` 保留历史。

README 不声称精确 tokenizer、cached/uncached token、cache-aware 或 GPU production result；旧 v0.10 plan 链接
改为本 canonical plan，Compose 示例的旧固定 `v0.10.13` 改为 `<version>` 占位，避免把历史 tag 冒充当前
recommendation。除上述 identity/说明和 R130 ledger 外，算法、threshold、defaults 与 simulation fixture 均不变。

exact release candidate 为：

```text
snapshot commit: 3df72de3c2f4c0900bbe66fa550d2fdd0008030b
tree:            3d2984a9a28233cd3ebb3c13a30ab88d9b30969f
archive SHA-256: eb23c25504401db336655ef7dd515333c2bb10a3e4d701054af9bb0e04e8e487
evidence SHA-256:c11b25505a9fd0998040ba0c251f49e89cb3906040bfabff32fe7770a62f64af
```

archive 创建时与当前 tracked worktree 精确一致，两个旧 untracked plan 未进入。授权 builder/container/Go 与
R129/R130 相同且 `running=true`、`OOMKilled=false`、restart=`0`。完整结果：

```text
FORMAT_CLEAN=1
TEST_EXIT=0
VET_EXIT=0
BUILD_EXIT=0
RACE_EXIT=0
SIMULATION_1_EXIT=0
SIMULATION_2_EXIT=0
SIMULATION_IDENTICAL_EXIT=0
SIMULATION_ACCEPTANCE_EXIT=0
BENCHMARK_KVADMISSION_EXIT=0
BENCHMARK_POLICY_MANAGER_EXIT=0
BENCHMARK_HTTP_EXIT=0
```

两次 simulation 继续为 `53,442` bytes、SHA-256
`87a4460206e372259e780f634dd4948ed647d47746d67859b65e00bc366c97bb`、byte-identical 且
acceptance=`passed`，证明 release identity/README 没有改变 deterministic verdict、regular multimodal burst、
exact 512K boundary 或 650K idle/busy/cancel/recovery。go test/race log SHA-256 为
`5d7f38ed10bfca84552b094efca88928ec089881a6219809d4120e992c1f2ee2` 和
`7daaa990c85fe10d10d0c8de3f8eaaeeaa5dd2695a87296d2096ec7669adb9dc`；gofmt/vet/build 仍为空文件
SHA-256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。

R131 benchmark 仍在 R130 noise range：hint text `6.929--7.010ns`、multimodal `3.708--3.740ns`；
Manager active 0/48/256 为 `148.9--149.1ns / 4.550--4.558us / 23.985--24.205us`，0 allocation；
HTTP fixture 为 `41.202--42.334us`、`39,323 B/op`、119 allocs。三组 benchmark log SHA-256 为
`1fc5403c935649675b68214cde425e778eddb977ce42260cd03d7dc223a37c5f`、
`78036e52b39d36135c3fbd9366981d8493787b5df1fa1987c6baba4872d96d60`、
`3ada922abba06662683cf33764a2ad6361b41d93e17844446fbdc50ab4365301`。这些仍是 builder CPU
microbenchmark，不是 GPU 服务延迟。

截至本节，完成层级为 v0.11.4 exact release source candidate + complete clean-builder matrix；尚未创建真实
source commit、push branch、annotated tag、builder-local/published image、Compose 或部署。追加本节后，下一步
必须证明相对 R131 tested tree 唯一变化为本 canonical ledger，审计 staged paths 只包含本 release 的 tracked
files，然后才可 commit/push/tag。image 必须从 clean pushed tag 构建并通过 OCI label、binary status 与
production image contract；当前仍禁止部署或 enable `use1-cb`。

### 13.76 R130 三遍 final review、post-admit telemetry corrective 与 exact matrix

本轮按 release gate 完成三遍复查，并在第二遍发现 executable 问题，因此没有直接继承 R129 full green：

1. **模型与因果。** `Cost.ApproximatePrefillTokenHint()` 在真实
   `requestAwareAdapterCost -> DecideRequestAwareAndReserve -> RequestAwarePolicy.Evaluate` 路径中使用；
   同一 snapshot 下，recognized modality 将 selection/prefill estimate 从 lexical marker/URL 尺度提升到
   已有 `EstimatedInputHigh`，能在 upstream forward 之前改变 verdict。hard-KV 仍用独立 safety upper。
   regular gate 计算 post-admit aggregate demand，严格 `>256K` 才保护；local pending exclusive 时短请求
   bypass regular aggregate gate。没有 cache、learner、模型名、tokenizer asset 或 Router/vLLM source 依赖。
2. **安全、生命周期与低流。** Manager 仍是唯一 reservation owner，check+reserve 位于同一 mutex；
   overflow/invalid/stale/preemption 先于 admit fail closed，prefill-complete/terminal/cancel/timeout/error 的
   既有 exact-once 生命周期不变。current pending 从同一 base/reservation registry 派生，不创建第二套计数器；
   regular 预算在无 pending demand 时不动作，exact 256K 允许，已有 exclusive 后的 short 允许，quiescent
   独占仍优先。第二遍发现原 R129 只覆盖 current sequence/token/long/quiescent，旧的无前缀
   `post_admit_pending_prefill_tokens` 在 drain 后仍保留 last decision 值，会继续造成“节点仍有 pending”的
   误读。R130 将该 gauge 也映射为 current registry tokens；历史 post-admit 只保存在显式
   `last_decision_post_admit_pending_prefill_tokens`。rejected request 不创建 reservation，因此 current
   post-admit 为 0，而 last-decision post-admit 仍保留本次反事实值。
3. **SOLID、效率、证据与发布。** estimator 只负责成本事实，pure policy 只负责 verdict，Manager 只负责
   原子 state/lifecycle，adapter 负责 transport mapping，metrics/status 只负责显式 current/last 发布；没有把
   CVM、Router 或模型逻辑塞进 domain policy。新增 current telemetry scan 只发生在 metrics/status scrape，
   不进入逐请求 decision hot path；为节省一次低频 O(n) scan 而合并 Manager ownership 与 transport snapshot
   会扩大接口和锁范围，当前没有 benchmark 证据支持该复杂化。完整 builder benchmark 证明 hot path 仍为
   0 allocation，R130 相对 R129 无可见回退。证据层严格停在 exact source candidate/full clean-builder
   matrix；没有把 deterministic simulation、microbenchmark 或旧 v0.11.3 image/live 证据外推为 v0.11.4
   production readiness。

R130 的最小 behavioral red 只把 adapter 恢复为 R129 的旧 post-admit 发布逻辑，同时保留新断言：

```text
snapshot commit: aa51497762bda0d2f7ab6b912670dfad024bea26
tree:            0768dbd76825ad695d375163ad4ebaae255ef3e5
archive SHA-256: 0e45446a97f1b6573bd6d0a623c3a2f8683bddb63cf65ef5bc2765f10787f171
evidence SHA-256:5388d1f1d1978d82ea7851b04436a84a14039556081103926f7b77ef58e17a03

FORMAT_CLEAN=1
POLICY_EXIT=0
ADAPTER_EXIT=0
TELEMETRY_EXIT=1
SIMULATION_EXIT=0
```

唯一失败断言显示 Manager reservations=`0`、current pending sequences/tokens=`0/0`，但旧无前缀
post-admit 仍为 `16,384`；failure log SHA-256 为
`30aa9b442cfd38e1836822f32b25be723870028275b3c96db16b2a5dd8a39dc6`。这证明 red 不是编译、fixture、
format、policy 或 simulation failure。

当前 exact candidate 为：

```text
snapshot commit: a0acdee25c7a3ccda318af57f88883eb4aac5ffc
tree:            b4b12cf176ac76dc10a5072641a960c73fdc5c3f
archive SHA-256: 72b19fb3ccb66f36023c961a39df77a4935108c9154bbbbb438845eedbf39a76
focused evidence:3ef9b4790bcd0084a191f75dccce3f11d68f30c2073ecea08fc43de4f983a7d1
full evidence:   2bbfc32f7f54b6cfd108abbbf62760af3c552964e3ce622288b3e903ac8614ce
```

focused format/kvadmission/policy/server/metrics/simulation 全为 exit 0。full matrix 同样全部通过：

```text
FORMAT_CLEAN=1
TEST_EXIT=0
VET_EXIT=0
BUILD_EXIT=0
RACE_EXIT=0
SIMULATION_1_EXIT=0
SIMULATION_2_EXIT=0
SIMULATION_IDENTICAL_EXIT=0
SIMULATION_ACCEPTANCE_EXIT=0
BENCHMARK_KVADMISSION_EXIT=0
BENCHMARK_POLICY_MANAGER_EXIT=0
BENCHMARK_HTTP_EXIT=0
```

两次 simulation 继续为 `53,442` bytes、SHA-256
`87a4460206e372259e780f634dd4948ed647d47746d67859b65e00bc366c97bb`、byte-identical 且
acceptance=`passed`；因此 telemetry corrective 没有改变任何 deterministic verdict 或 512K/650K 合同。
主要 material log SHA-256 为：go test
`e4162164b140b8e6ca7947c4db8dbb249db066c8e5c868e50a15964390b57d1e`，race
`fc83d1b79abd8ccee7160af804362596b5ac6806da9d1505840ac262e6fc22b4`，gofmt/vet/build 均为 empty-file
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。

R130 builder CPU benchmark（全部 applicable pure/Manager 项 0 allocation）：hint text
`6.921--6.975ns`、multimodal `3.709--3.737ns`；full estimator 1KiB/64KiB/1MiB/2MiB 为
`269.4--275.8ns / 1.960--1.978us / 28.411--28.740us / 62.957--64.051us`；bounded lexical
`87.25--90.91ns`；pure policy `17.48--47.75ns`；Manager active 0/48/256 为
`149.0--149.2ns / 4.541--4.641us / 24.079--24.103us`。HTTP fixture 为
`41.259--41.927us`、`39,322--39,323 B/op`、119 allocs。benchmark log SHA-256 分别为
`ae3bde5ab2eb66797ab688f24277e3af260f8d562bc40c29d3380384f6a691b7`、
`07d340923aba0dce6a84dbdf59ee29c86a93a6c966fe79628f9e5194f5770649`、
`883870e3af9e4aff30fa22f77adbca1fb90d338fdee0e9d682c62ddacd53e484`。HTTP 仍是测试 transport
全路径，不是 pure policy 或 GPU latency。

R130 archive 只包含 tracked files，两个旧 untracked plan 未进入；archive 时当前 tracked worktree 与
`a0acdee...` byte-for-byte Git-clean。随后只追加本 ledger 和 review 结论，因此下一步必须先证明相对 R130
唯一 drift 为 canonical plan，再管理 runtime/README/OCI `v0.11.4` identity，并对新 exact source 重新跑
适用 builder/release/image contract。当前仍未 commit/push/tag/image、未修改 Compose、未部署或重新 enable
`use1-cb`。

### 13.78 R132/R133 v0.11.4 source tag、builder-local 与 registry immutable image provenance

提交前对全部 tracked release changes 执行 staged-path、`git diff --cached --check`、secret scan 与远端
drift audit；两个旧 untracked plan 继续保持 `??`，未进入 index。prospective index tree 相对 R131 tested
tree 除本 canonical ledger 外无差异：

```text
R131 tested tree: 3d2984a9a28233cd3ebb3c13a30ab88d9b30969f
source commit:     c6e8ac37f3e490d12eef06e08bc1908b69078ee1
source tree:       3e308b4d111675d2c6a7ca49a01ae5123bb6839d
branch:            codex/pig-v0.11.0-request-aware
annotated tag:     v0.11.4
tag object:        28b06970ef463836ddd16f1c3c723d856f798b61
tag dereference:   c6e8ac37f3e490d12eef06e08bc1908b69078ee1
```

branch push 后用 `ls-remote` 重新证明远端 branch 与 local HEAD 精确一致；tag push 成功，GitHub API 也证明
`refs/tags/v0.11.4` 是上述 annotated tag object。tag 不会移动；本节是 tag 发布后的 docs-only ledger。

GitHub `Publish Image` run #26（run ID `31113029042`，job `92655438941`）绑定
`head_branch=v0.11.4`、`head_sha=c6e8ac37...`。checkout、GHCR login 与
`EXPECTED_VERSION=v0.11.4 tools/validate-production-image-contract.sh` 均成功。buildx 完成 build、上传所有
layers，并打印 `#16 DONE 5.6s`，随后对
`https://ghcr.io/v2/phala-network/phala-inference-guard/manifests/v0.11.4` 执行 HEAD 时得到
`denied: denied`，因此 run conclusion=`failure`。这不是 Go build 或 image-contract failure；但红 workflow
仍是必须显式关闭的发布流程异常，不能因 artifact 后续可拉取就隐藏。

授权 builder preflight：container `pig-v01011-builder` 为 `running=true`、`OOMKilled=false`、restart=`0`，
Go `1.24.13 linux/amd64`，builder host Docker `25.0.3`。R132 从远端 `v0.11.4` 在 builder container
内 clean clone，证明：

```text
head=c6e8ac37f3e490d12eef06e08bc1908b69078ee1
tree=3e308b4d111675d2c6a7ca49a01ae5123bb6839d
tag_object=28b06970ef463836ddd16f1c3c723d856f798b61
tag=v0.11.4
status_lines=0
```

builder-local image 结果：

```text
image:       pig-v0114-clean-tag-r132:local
image ID:    sha256:105e9f91710fdd1414aae6e58d821531449da5ec926f0e57eddb414a04c7b9f6
size/arch:   29,262,543 bytes / amd64
OCI version: 0.11.4
entrypoint:  ["/phala-inference-guard"]
binary:      b598d85c50d197b961a8366fbc34c00628acf381d4eb64ea5f1f09b22b0dadab
contract:    PIG_PRODUCTION_IMAGE_CONTRACT_OK
startup:     PIG-v0.11.4 / running=true / exit=0 / restart=0
archive:     tmp/v0114-release-20260806-r1/pig-v0114-r132-local-image-provenance-slim.tar.gz
archive SHA-256: 90df3c658f6fedc2f872be4b53fda07bf816d59a29a8691b3c6280ae4f35a810
```

R132 原 harness 在所有 image/contract/startup gates 完成后，因 builder host BusyBox `xargs` 不支持 `-0`
而只在证据 manifest 封存处退出 1。它没有否定前置结果；corrective seal 在逐项验证既有 source/image/log/
binary preconditions 后，以兼容的 shell loop 生成 `SHA256SUMS` 并封存，未重建或改写镜像。原 runner 与
corrective seal SHA-256 分别为
`8c0461a19ab7a8d128e16fbb57b3ce1bfb17e2d8037e1f902fd812be974c6c52`、
`ce0ee1a649fa91e513e31797a59525a82c5101b29364913e393358ec172300bc`；下载后的 archive 与内部 manifest
均重新验证通过。

尽管 workflow status 为 red，R133 在同一授权 builder 实际 pull `v0.11.4` 成功，并将 tag 锁定为：

```text
immutable digest:
  ghcr.io/phala-network/phala-inference-guard@sha256:b8756c49271d7ac0c42f46cd0201db571cd02bce1c08e3721fafe8ae0a2e016e
registry image ID:
  sha256:6bfc9e7aecd14501eb2660cf29bc359ed98d698e43990c19ad89a0a8a65531d6
size/arch:   29,262,543 bytes / amd64
OCI version: 0.11.4
entrypoint:  ["/phala-inference-guard"]
contract:    PIG_PRODUCTION_IMAGE_CONTRACT_OK
startup:     PIG-v0.11.4 / running=true / exit=0 / restart=0
```

registry binary SHA-256 同样为
`b598d85c50d197b961a8366fbc34c00628acf381d4eb64ea5f1f09b22b0dadab`，与 clean-tag builder-local
binary `cmp=0`。R133 runner SHA-256 为
`3441d645d6312a5f116d202b6f52b99fb8c2a53bd0cb20073ecbf3d28c0d9383`；registry evidence archive 为
`tmp/v0114-release-20260806-r1/pig-v0114-r133-registry-image-provenance-slim.tar.gz`，SHA-256
`77d8a80ffe3426ee11db7a6edadf7dddeacdf0d5caf2e2e193d9349b99e93fe0`，下载后 archive 和内部
manifest 均通过复核。

因此 v0.11.4 当前完成层级为 source implementation、完整 clean-builder matrix、commit/push/annotated
tag、builder-local image、published registry immutable image 与 binary provenance。尚未完成 workflow 红状态
关闭/例外、Compose integration、`use1-cb` Router-disabled deployment/readiness、Router canary 或 30 分钟生产
流量观察；在下一 gate 前继续禁止部署、发送实际推理请求或重新 enable Router。

### 13.79 R134 v0.11.4 部署前只读 live drift audit 与精确候选

在未重跑 GitHub workflow、未部署、未修改 Router、未发送 chat/completions 的前提下，于
`2026-08-06T15:15:46.2400540Z` 对 `use1-cb` 执行当前 live 只读采集。collector 只调用
Router/CVM/container logs、authenticated `/v1/models`、PIG/vLLM metrics、attestation 与对应未认证 metrics
边界；证据目录与 summary 为：

```text
tmp/pig-v0113-use1-cb-live-20260806/pre-v0114-readonly-20260806T151545Z
summary SHA-256: b39a50c0894e0d8438d238113888512172582fe07688ef38c366148981743fad
```

当前 live identity 和 drift baseline：

```text
CVM: gemma4-31b-it-use1-cb
status/in_progress: running / false
live Compose SHA-256: 0c6debae711a56c45117f4d3f951e2ab0cdd58be7630721d8bdea21a5f3a6775
PIG image: ghcr.io/phala-network/phala-inference-guard@sha256:15d827456c56a534d71b03932d5a9a90d2d7984e5cbfec6aec3b2632cfcc0d99
vLLM image: ghcr.io/phala-network/vllm-openai:v0.24.0-cu129-ubuntu2404-phala.8@sha256:485ec89ea08e6b4ead55f4721b01c053264d747bde685de04cd7d5b114d219fe
PIG/vLLM container: running / running, both Up 5 hours
Router enabled set: use1-19,use1-9b
use1-cb upstream/route enabled: false / false
use1-cb route running: 0
```

authenticated models/PIG metrics/vLLM metrics/attestation 均为 HTTP 200，PIG/vLLM metrics 未认证均为 401。
PIG runtime 为 `PIG-v0.11.3`、predictive mode=`enforce`、intake open=`1`、TTFT enabled=`0`；Manager
reservations=`0`、forwarded pending=`0/0 tokens`，vLLM running/waiting/KV=`0/0/0`，PIG failure、vLLM
preemption/error 和 30 分钟 PIG/vLLM engine/GPU fatal log match 全为 0。当前 live 配置继续为：

```text
DYNAMIC_TTFT_ENABLED=false
PIG_QUEUE_WAIT_SECONDS=0
PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS=500
PREDICTIVE_MAX_METRICS_AGE_MS=1500
PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS=500
PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS=300000
PREDICTIVE_TPS_TARGET/FLOOR=25/20
KV_ADMISSION_MODE=off
KV_ADMISSION_VLLM_TARGET/HARD_RATIO=0.84/0.88
vLLM max-model-len/max-num-seqs/max-num-batched-tokens/gpu-memory-utilization=262144/512/8192/0.91
```

collector 的 `all_passed=false` 不能概括为 live unhealthy。两个 false 分别是：

1. v0.11.3 旧无前缀 request-aware pending gauge 仍显示最后一次 decision snapshot
   sequences/tokens=`33/168940`，但唯一 Manager reservations、forwarded pending 和 vLLM running/waiting
   都是 0；这正是 v0.11.4 R130 已用 current registry telemetry 修复的语义问题；
2. 节点 Router disabled 且最近 30 分钟 idle，因此该窗口没有新的 enforced protection log；这不是 failure、
   preemption、fatal 或 intake closed。

以该 exact live Compose 为 rollback source，R134 在本地 `tmp/` 机械生成两个未部署 candidate。生成器：

```text
tmp/pig-v0114-use1-cb-live-20260806/prepare-v0114-candidates.ps1
SHA-256: aa2f8e92c49919a70ce0ba9c899baa2713911c4c87cbc287a5424a53a05f9b6f
```

候选结果：

```text
rollback source:
  tmp/pig-v0113-use1-cb-live-20260806/pre-v0114-readonly-20260806T151545Z/live-compose.yaml
  SHA-256: 0c6debae711a56c45117f4d3f951e2ab0cdd58be7630721d8bdea21a5f3a6775

shadow candidate:
  tmp/pig-v0114-use1-cb-live-20260806/predeploy-r1/use1-cb.v0114.shadow.candidate.yaml
  SHA-256: 92d55f600970b0c3f95dd465756012243816bfd032805e7a34952eeff890e196
  exact changes: PIG digest 15d827... -> b8756c...; predictive mode enforce -> shadow

enforce candidate:
  tmp/pig-v0114-use1-cb-live-20260806/predeploy-r1/use1-cb.v0114.enforce.candidate.yaml
  SHA-256: 711f20570159c82666fd9e0827ac7c8de8aaa5d0aaba880e95734e93d3f5a3c7
  exact changes: PIG digest 15d827... -> b8756c...

candidate summary SHA-256:
  ffda4a145c19360bac41da282fcab1af990745b42fa63e690b54b2f2f6f60ff5
```

`git diff --no-index` 证明 shadow 只有四条加减行、enforce 只有两条加减行；除 immutable digest 和
shadow mode 外不存在服务、vLLM 参数、TPS、500ms poll、1500ms age、TTFT、prefill contract 或其他
Compose drift。候选 secret scan 未发现 Router token；summary 显式记录 `deployed=false`、
`router_changed=false`、`inference_requests_sent=0`。GitHub workflow #26 红状态 gate 仍未关闭，因此 R134
只完成 live baseline 和 candidate preparation，不授权部署。

### 13.80 R135 v0.11.4 Router-disabled live harness 与三遍静态复查

R135 只在 root checkout 的 local-exclude-covered `tmp/pig-v0114-use1-cb-live-20260806/` 编写未来部署阶段的验证
harness，没有执行其中任何一个脚本，没有部署 Compose、重启容器、修改 Router 或发送推理请求。公共
模块把 token、HTTP、Prometheus snapshot、current/last telemetry、日志计数和 secret scan 分开；各 runner
只负责一个 gate，避免继续复制 v0.11.3 中已失效的 gauge 语义。执行顺序与 stop rule 记录在：

```text
tmp/pig-v0114-use1-cb-live-20260806/HARNESS_PLAN.md
SHA-256: 1c1ee0047ce777b2b379ba2ee8c365c8b25743b46d8f209d321269de8df018cc
```

当前 harness identity：

```text
v0114-live-common.ps1
  c8fb48a302aed5908e3ff678830fa54f687a03cfdb36e07b0b793a52cc5422c9
capture-v0114-preflight.ps1
  9b563e4f6ef3ac7a28ab7dbe57dd5d645f7201cbadf005b636e0d5c93cced637
run-v0114-protocol.ps1
  10e482e6a937ca24fdef41935a096db2d56795b8a6b7f876db9ff7d2f51fa1bf
run-v0114-lowflow.ps1
  aa70e1c1a7d6e62f2399e01dcafc13e3712a88a4980dff80810a105a025de269
run-v0114-shadow-size-tiers.ps1
  9c16b93120fe7c16815e996ae7ece52938b8e14a3004c709f54bdd4b44612a6a
run-v0114-enforce-weighted-budget.ps1
  527bceec4609fcc9b4925f0af7d2dd9b1c0cb691f4ad75193bc3956b442df569
run-v0114-enforce-exclusive.ps1
  a00b53967def59ba44034f721da9d4d52b888a7e458aeae7df26331f5400ac9b
```

三遍复查均在不执行 live runner 的边界内完成，并在发现问题后先修正再进入下一遍：

1. **模型与因果。** 首版 weighted runner 继承了“第三个并发 Task 必然先返回 429”的隐含假设；真实
   调度不保证完成顺序。现改为 `Task.WaitAny` 接受任意最先完成的 causal 429，并在两个 weighted
   reservation 仍存在的同一压力窗口发送一个小请求，要求“大请求 429、小请求 200”、Router projection
   仍 open、429 counter 和 unified protection log 各精确增加一。v0.11.4 operational
   `post_admit_pending_prefill_tokens` 只按 current Manager registry 验收；超过 256K 的第三请求
   counterfactual 只从显式 `last_decision_post_admit_pending_prefill_tokens` 验收，不能恢复 v0.11.3 stale
   读法。
2. **安全与生命周期。** 首版 preflight 错误假设 `/v1/upstream-status` 返回详细 status string；源码合同实际
   只返回 `0/1/2/3`。现要求 endpoint 为 green=`0`，另从最新周期状态日志独立验证
   `prefill_current=0/0/0/0` 和 `prefill_last` 拆分。所有 request runner 都要求 pre/post current
   reservation、forwarded pending、五个 current Prefill gauges、vLLM running/waiting/KV 清零；低流覆盖
   首请求、串行、completion window、取消恢复和 12 请求 burst。shadow size runner 只发送约 80K、230K
   text 和一个可解码的 1x1 PNG，不复制 40-request simulation burst；最大实际 word-count 为 230000，
   没有向 262K 节点构造 512K/650K prompt。
3. **证据与发布。** PowerShell AST parse 对上述七个 runner/module 全为 0 error；静态 command audit 未发现
   `deploy`、CVM update/delete/create、Router 或 Compose-up 写命令。candidate hashes 仍为 shadow
   `92d55f...`、enforce `711f205...`、summary `ffda4a...`，未发生漂移。`2026-08-06T15:47:06Z`
   只读 GitHub API 复查确认 workflow `31113029042` 仍为 attempt `1`、`completed/failure`、head
   `c6e8ac37...`；远端 branch 仍为 `66a2932...`，annotated tag object/dereference 仍为
   `28b0697...`/`c6e8ac3...`。没有重跑 workflow，也没有把 registry artifact green 解释为 workflow 或
   deployment green。该遍还发现 root `tmp/` 默认并未 ignore；只在本地 `.git/info/exclude` 增加精确
   `/tmp/pig-v0114-use1-cb-live-20260806/`，没有改共享 `.gitignore` 或隐藏其他路径，随后
   `git check-ignore -v` 对 common/preflight/plan 三个代表文件均命中该精确规则。

R135 只达到 **live validation design/static readiness**，不增加 Compose integration、deployed runtime、
readiness 或 production evidence。下一步 gate 不变：必须先关闭 workflow 红状态，或由用户显式接受该发布后
校验异常；随后重新读取 live CVM/Compose/Router drift，才可部署 exact shadow candidate。shadow 与 enforce
各阶段必须使用新 harness 的全新 evidence directory，任何失败立即停止，禁止直接 enable Router。

### 13.81 R136 v0.11.4 canary observer、即时回退与三遍静态复查

R136 仍只修改 root checkout 中被精确 local exclude 的
`tmp/pig-v0114-use1-cb-live-20260806/` harness，并把审计结果追加到本 canonical plan。没有运行任何
PowerShell runner，没有部署 Compose、重启容器、修改 Router 或发送推理请求。R135 后又确认 PIG 同签名
protection log 会在 1 秒窗口节流并以 `suppressed=N` 表示被折叠事件，因此 physical log line 不能与 429
counter 一一对应：精确事件数只读
`pig_predictive_admission_enforced_rejects_total`；日志验收只要求 counter 增长后至少出现一条新的 unified
`enforced=true` 行，并把 `1 + suppressed` 作为独立的 represented-event 诊断量。该修正使 R135 的 common、
low-flow 和 weighted runner 哈希成为历史值，本节的哈希取代它们。

当前完整 harness identity：

```text
HARNESS_PLAN.md
  1c1ee0047ce777b2b379ba2ee8c365c8b25743b46d8f209d321269de8df018cc
CANARY_PLAN.md
  117526cfa64a2295e6ad6246ca168a596c86faa5ffda11e32a25b4365544fc0d
v0114-live-common.ps1
  448a7b0fdcdb776a7c2bb1a795df95c30f35319ff78c6b35798a405e5c1142da
capture-v0114-preflight.ps1
  9b563e4f6ef3ac7a28ab7dbe57dd5d645f7201cbadf005b636e0d5c93cced637
run-v0114-protocol.ps1
  10e482e6a937ca24fdef41935a096db2d56795b8a6b7f876db9ff7d2f51fa1bf
run-v0114-lowflow.ps1
  8a5582c1b5e9b23e323b21f1b29fe0906ab292aea49f2a43f6972677990a6d3b
run-v0114-shadow-size-tiers.ps1
  9c16b93120fe7c16815e996ae7ece52938b8e14a3004c709f54bdd4b44612a6a
run-v0114-enforce-weighted-budget.ps1
  a79dc0fb40245dde9ac94d8f250db4b8887cde8048014a6394cfd7793bc2b24d
run-v0114-enforce-exclusive.ps1
  a00b53967def59ba44034f721da9d4d52b888a7e458aeae7df26331f5400ac9b
observe-v0114-canary.ps1
  2604ef894e2c3edfee00a0975c4f8e892ece99b0ac2ab6757d7ba66452d62646
read-v0114-canary-progress.ps1
  3d7eea7a0feb14442977f5bb9cb503a5802b4b62d01af4503054fb90b97f795b
```

三遍复查发现问题后均先修改 harness，再从头执行 PowerShell AST parse；最终十个 `.ps1` 文件均为
`0` parse error：

1. **模型与因果。** R128 失败形状 `running=52、pending Prefill≈41` 在 observer 中产生约 `11`
   个 estimated Decode sequences；prompt/generation counter 同时冻结且 generation TPS 近零时，30 秒
   Decode-freeze stop 可触发。单个纯 Prefill 的 `running=1、pending=1` 则得到 `0` Decode，不会由该规则
   误停；短 TPS 下降仍分别需要连续 30/120 秒。复查发现初版直接把
   `pig_dynamic_observed_single_user_tokens_per_second` 当逐请求 TPS 且没有 validity gate，会把启动/低流的
   无效 `0` 样本误作 QoS failure。源码确认它是 aggregate generation capacity 除以 Decode concurrency；
   现明确命名为 mean-active TPS proxy，只有 predictive forecast valid、dynamic Decode active 且无 Prefill
   transition 时才进入 TPS stop，真实 request-level TPS 保留为独立 live-analysis 证据。completion goodput、
   prompt/uncached-prompt、cache-token/prefix-block mix、Router share 和 estimator/prediction 平均微秒数分别
   记录；TTFT 只观测，不是 stop/admission gate。PIG、vLLM 或 target-route cumulative counter 回退会立即
   stop，避免 runtime restart 后用跨 epoch 差值伪造 green。
2. **安全与生命周期。** 初版在 stop 后先抓完整 PIG/vLLM logs、随后才 disable，可能延迟真实故障回退。
   现有 live stop 或 interruption 在进入 `finally` 后先执行唯一允许的
   `PATCH /v1/admin/upstreams/use1-cb {"enabled":false}`，再抓日志/保护证据；只有 post-window evidence gate
   首次产生 stop 时才在采集后回退，`rollbackHandled` 保证不重复 PATCH。初始 protection-log 失败也进入
   stop/finally，不再在回退保护之外直接 throw；log collector 异常被转换为 post-window failure，不能跳过
   late rollback。disable 后同时验证 upstream/route false、其他 enabled state 与 route inventory 保留；
   drain 新增 forwarded pending safety-token gauge，并只读 current Manager gauges，不读取
   `last_decision_*`。只有 confirmed-disabled 才等待最多 10 分钟的 Router/PIG/vLLM/KV drain；回退不确定时
   立即记录失败供 operator 处理，不用十分钟假装仍在 drain。
3. **证据与发布。** 静态 command audit 对十个脚本得到 `PATCH=1`、`enabled=false payload=1`、
   `enabled=true=0`、deploy/Compose-up=`0`、TTFT stop reason=`0`；两个 rollback call site 分别属于
   live-stop 和 post-window-stop，均受同一 exact-once state 保护。secret-literal scan 对 harness 顶层十二个
   文件无命中；root `.git/info/exclude` 仍只精确覆盖该 harness 目录。shadow/enforce/summary candidate
   SHA-256 继续为 `92d55f600970...`、`711f20570159...`、`ffda4a145c19...`，最大实际构造仍为 230000
   words，不向 262K `use1-cb` 发送 512K/650K actual prompt。nested branch/remote branch 为
   `88c963bf...`，annotated tag object/dereference 保持 `28b06970...`/`c6e8ac37...`，tag 未移动。
   `2026-08-06` 只读 GitHub API 再确认 Publish Image run `31113029042` 仍为 attempt `1`、
   `completed/failure`、head `c6e8ac37...`；没有重跑 workflow。

R136 只达到 **canary observation/rollback harness static readiness**。它不增加 v0.11.4 Compose
integration、deployed runtime、endpoint readiness、Router canary 或 30 分钟 production evidence，也不能
把 mean-active TPS proxy 冒充真实逐请求 TPS。发布 workflow 红状态 gate 仍必须先关闭或获得显式例外；
之后仍从 fresh CVM/Compose/Router drift recheck、Router-disabled shadow/enforce 和 final preflight 开始，
不能直接 enable `use1-cb`。

### 13.82 R137 Publish Image #26 原始记录、根因边界与一次性例外建议

R137 只进行 GitHub/GHCR 发布证据的只读复核并更新 canonical ledger。没有修改 workflow、tag 或 registry，
没有重跑/dispatch workflow，没有部署 Compose、重启容器、修改 Router 或发送推理请求。公开 GitHub API
确认 run `31113029042`、job `92655438941` 仍为 attempt `1`、`completed/failure`，tag/source 仍为
`v0.11.4`/`c6e8ac37...`；authenticated job-log/artifact download 只复用本机已能 push 该仓库的 credential，
token 仅存在于进程内，未写文件或回显。

原始证据 identity：

```text
job log:
  bytes:   47215
  SHA-256: c59dcd047bd2ce054780fc3a305b8ea1be2ccbdd7802ef9e4c975c0ae13f0cb9

GitHub artifact 8972511190 / Phala-Network~phala-inference-guard~29OVRC.dockerbuild:
  encoded bytes:   11565
  encoded SHA-256: 93c4b03474c39d7c5ef4d8fe9975cf80d53d307e3cd0ec710227e646e95a3a6b
  decoded bytes:   105472
  decoded SHA-256: dd03a09a3b60b2dabd571652bfd13c4acb1ab8cc0f926e58445c0bc5db9bbcc9
```

三遍复查结论：

1. **构建/发布因果。** workflow source 在 tag 与当前 branch 完全相同：checkout、GHCR login、production
   image contract 均先成功；失败发生在 `docker/build-push-action@v6` 内部。raw log 显示 BuildKit 完成 image
   export、以 `v0.11.4` 命名、上传全部 layer，最后由 Docker daemon 对该 tag manifest 做 HEAD 时得到
   `denied: denied`。这不是 workflow 中另写的一条 post-validation command，也不是 Go build、contract、
   checkout 或 login failure。相同 workflow/credential path 在约五小时前的 v0.11.3、此前的 v0.11.2 与多数
   历史版本均成功，现有证据不支持把它归类为稳定可复现的 YAML 或永久 package-ACL 缺陷。
2. **artifact identity 与安全。** `.dockerbuild` record 内 `writing image` config digest 为
   `sha256:6bfc9e7aecd14501eb2660cf29bc359ed98d698e43990c19ad89a0a8a65531d6`；R133 从 registry
   immutable digest 拉取后的 image ID 也是同一 digest，二者 exact match。R133 又证明 registry binary
   SHA-256 `b598d85c...` 与 clean-tag builder-local binary `cmp=0`，OCI version、entrypoint、production
   contract 和 startup 均一致。因此 registry artifact 不是“只看到 layer 就推测发布成功”，而与 run #26
   构造的 exact image config 和已测试 binary 双重闭合。build record 同时显示 config `created` 为
   `2026-08-06T14:53:10.565235018Z`；workflow 没有固定 `SOURCE_DATE_EPOCH`。擅自重跑会重新构造带新
   timestamp 的 config，可能改变 image/manifest digest 并移动已锁定的 v0.11.4 tag，因此重跑不是无副作用
   的验证动作，也不是当前首选修复。
3. **发布边界。** workflow conclusion 仍客观为 red，不能篡改成 success；但 source/tag、contract、run
   build record、registry immutable image、image config、binary 和 startup 已形成足够强的 exact
   cross-provenance。本计划因此把最小且不破坏 immutable provenance 的下一步收敛为：由用户显式接受
   “run #26 terminal GHCR HEAD anomaly”作为 **v0.11.4 一次性发布例外**。这只关闭进入 Router-disabled
   deployment 的 release-process gate，不把 image 证明外推为 deployment/readiness/canary green。未来版本
   是否增加 reproducible timestamp、OCI revision/source labels 与 authenticated digest verification 属于独立
   CI hardening，不能为了消除旧 run 的红色外观临时改 workflow 后移动 v0.11.4 tag。

截至 R137，仍未获得该一次性例外的用户授权，所以不进入 actual deployment。授权后也必须重新读取 live
CVM/Compose/Router state，从 exact v0.11.4 shadow candidate 的 Router-disabled 部署和全套 harness 开始；
任何 drift 或 gate failure 都立即停止，不能跳过 shadow/enforce/final-preflight 直接 enable Router。

### 13.83 R138 v0.11.4 terminal HEAD 非阻断记录与执行恢复

2026-08-07 重新核对仓库 workflow、canonical ledger 与 GitHub Actions 公开 API 后确认：

- `.github/workflows/publish-image.yml` 从仓库初始版本起由 `v*` tag push 自动触发，并使用
  `docker/build-push-action@v6` 向 GHCR 发布；
- GitHub `Publish Image` 对 `v0.8.13`、`v0.11.0`、`v0.11.2`、`v0.11.3` 均为成功；`v0.11.1`
  在 production image contract 阶段失败且没有发布；`v0.11.4` 是已记录的 terminal manifest HEAD
  anomaly；
- R132 只在授权 builder 从 clean public tag 构建并验证 builder-local image，R133 只从 registry
  pull tag/digest 并执行 contract、startup 与 binary identity 验证；没有证据显示 R132/R133 从 builder
  对 `v0.11.4` 执行过 `docker push`；
- run #26 `.dockerbuild` config digest 与 R133 registry image ID exact match，因此现有 immutable registry
  artifact 与该 CI 构建精确对应；workflow conclusion 仍保持 `failure`，不能篡改为 success。

用户在上述发布路径澄清后指示“继续”。本计划据此只把 run #26 terminal HEAD anomaly 从硬阻塞 gate
改为 **v0.11.4 一次性非阻断 release-process 记录**。这一决定不授权也不暗示重新构建、重跑 workflow、
覆盖 registry tag 或移动 annotated tag；现有部署候选继续只引用 immutable digest
`sha256:b8756c49271d7ac0c42f46cd0201db571cd02bce1c08e3721fafe8ae0a2e016e`。

执行从 fresh source/tag/digest、CVM、Compose、Router inventory 和 enabled-set drift recheck 恢复。只有目标
仍为 `a0f0bfb3-e46f-4b22-814e-24872f251193` / `use1-cb` 且 upstream/route 均 disabled，才允许部署
exact v0.11.4 shadow candidate；shadow、enforce 与 final preflight 任一 gate 失败都立即停止并保持 Router
disabled。只有完整 Router-disabled 验证 green 后，才可按第 12 节执行单节点 enable、带自动 disable 的
30 分钟观察与事后分析。

未来版本只能选择一个 canonical registry publisher：若采用授权 builder publication，则必须先把 tag-triggered
CI 改为 validation-only 或取消重复 push；若保留 CI publication，则 builder 继续只做独立构建和 registry
回拉验证。禁止两个环境用含动态 timestamp 的独立构建竞争覆盖同一版本 tag。该 future CI/release
hardening 不在 v0.11.4 live validation 的执行范围内。
