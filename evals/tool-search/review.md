# Tool search 候选题审核

状态：全部待审核。人物、聊天、地点、天气与地图响应均为人工编写的合成数据，不含真实用户原文。

以下内容审核通过后，由执行方复制并冻结新的reviewed题集；当前草稿不会自动变成正式参考。

## 需要确认的三项

- 20道题的业务预期及逐项人工检查是否合理；哪些题需要修改。
- 是否允许使用当前云模型 `cloud-qwen-max-2026-06-08`，先6次冒烟、再180次完整对照，并将合成问题、参考、回答和指标上传到自部署Phoenix。每个Agent执行可能有多次模型请求，费用上限尚未确认。
- 建议验收线：关键任务无新增失败、整体完成率不下降、完整执行的真实总token至少减少15%、典型耗时（p50）增加不超过20%。这些数值是建议，不作为已批准标准。

上线方案与部署另行决定。本轮仅准备和离线验证。

## 可用历史证据

- 2026-10-29：小甲提议周六下午三点在青禾公园北门见面；小乙确认。小甲说车票总共286元。
- 2026-10-31上午：确定去苏州，每人预算300元，小乙买车票。
- 2026-10-31晚间：小甲洗碗，小乙倒垃圾。
- 每题时间固定为2026-11-01 10:00（北京时间）。夹在旧证据与提问之间的35条背景消息用于让历史检索题无法直接看到答案。
- 2026-11-02的“四点、999元”是截止边界之外的干扰消息，不能作为输入或答案。

## 20道题

### 1. chat-greeting（聊天与写作）

问题：你好，今天给我一句鼓励吧。

候选参考：简短友好鼓励，不创建提醒、不查询地图。

必须工具：respond。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：无；人工核对。
来源断言：0条指定原文。

- [ ] 自然、有帮助；未编造用户经历。
- [ ] 业务预期已确认（当前待审核）。

### 2. chat-goodnight（聊天与写作）

问题：帮我写一句温柔的晚安，不超过30个字。

候选参考：一条不超过30字的温柔晚安。

必须工具：respond。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：晚安。
来源断言：0条指定原文。

- [ ] 人工数中文字符，不超过30；无需历史检索。
- [ ] 业务预期已确认（当前待审核）。

### 3. chat-rewrite（聊天与写作）

问题：把“今天有点累，但还是想见你”改写得温柔一些。

候选参考：保留疲惫但想见面的意思。

必须工具：respond。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：无；人工核对。
来源断言：0条指定原文。

- [ ] 没有把改写文本视为创建提醒指令。
- [ ] 业务预期已确认（当前待审核）。

### 4. chat-knowledge（聊天与写作）

问题：为什么天空通常看起来是蓝色的？用两句话解释。

候选参考：以通俗语言解释短波长光更易散射。

必须工具：respond。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：散射。
来源断言：0条指定原文。

- [ ] 原理正确，约两句话；不编造聊天历史。
- [ ] 业务预期已确认（当前待审核）。

### 5. history-meeting（历史问答与总结）

问题：上次我们约好周六几点、在哪里见面？

候选参考：周六下午三点，青禾公园北门。

必须工具：answer_question。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：三点, 青禾公园, 北门。
来源断言：1条指定原文。

- [ ] 正确归属原约定，未读取未来“四点”；原证据在初始30条窗口之外。
- [ ] 业务预期已确认（当前待审核）。

### 6. history-fare（历史问答与总结）

问题：我上次说买车票一共花了多少钱？

候选参考：286元。

必须工具：answer_question。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：286。
来源断言：1条指定原文。

- [ ] 来源为小甲本人原文，不能混用未来999元。
- [ ] 业务预期已确认（当前待审核）。

### 7. history-yesterday-trip（历史问答与总结）

问题：总结昨天上午我们确定的出行安排和分工。

候选参考：2026-10-31上午：去苏州，每人预算300元，小乙买车票。

必须工具：summarize_history。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：苏州, 300。
来源断言：2条指定原文。

- [ ] 时间范围为2026-10-31上午；不混入29日或昨日晚间家务；分工正确。
- [ ] 业务预期已确认（当前待审核）。

### 8. history-yesterday-chores（历史问答与总结）

问题：总结昨天晚上我们约定的家务分工。

候选参考：小甲洗碗，小乙倒垃圾。

必须工具：summarize_history。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：洗碗, 倒垃圾。
来源断言：2条指定原文。

- [ ] 时间范围为2026-10-31晚间；角色正确，不把分工自动建成提醒。
- [ ] 业务预期已确认（当前待审核）。

### 9. reminder-tomorrow（提醒）

问题：明天上午九点提醒我喝水。

候选参考：创建2026-11-02 09:00北京时间的一次性喝水提醒。

必须工具：set_reminder。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated。

关键词断言：创建, 09:00, 喝水。
来源断言：0条指定原文。

- [ ] 检查set_reminder输入date=2026-11-02、time=09:00；模拟操作数=1；没有真实投递。
- [ ] 业务预期已确认（当前待审核）。

### 10. reminder-absolute（提醒）

问题：2026年11月3日晚上八点半提醒我拉伸。

候选参考：创建2026-11-03 20:30北京时间的一次性拉伸提醒。

必须工具：set_reminder。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated。

关键词断言：创建, 20:30, 拉伸。
来源断言：0条指定原文。

- [ ] 检查date=2026-11-03、time=20:30、标题拉伸；模拟操作数=1。
- [ ] 业务预期已确认（当前待审核）。

### 11. reminder-missing-time（提醒）

问题：明天提醒我买牛奶。

候选参考：保留待补充草稿，追问具体时刻，不默认时间。

必须工具：set_reminder。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated。

关键词断言：几点。
来源断言：0条指定原文。

- [ ] 检查date=2026-11-02、time为空；结果needs_clarification；不得声称已安排投递。
- [ ] 业务预期已确认（当前待审核）。

### 12. reminder-recurring（提醒）

问题：以后每天早上九点提醒我喝水。

候选参考：解释仅支持一次性提醒，请用户改为一条具体提醒。

必须工具：set_reminder。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated。

关键词断言：一次。
来源断言：0条指定原文。

- [ ] action=unsupported；模拟写入数=0；不得创建一次性任务来冒充周期提醒。
- [ ] 业务预期已确认（当前待审核）。

### 13. maps-weather（地图）

问题：查一下上海今天的天气。

候选参考：仅依据合成预报：2026-11-01晴，最高22℃，最低16℃。

必须工具：maps_weather。
禁止工具：set_reminder。

关键词断言：晴, 22, 16。
来源断言：0条指定原文。

- [ ] 说明天气来源；不把次日小雨当今天；天气数据为模拟。
- [ ] 业务预期已确认（当前待审核）。

### 14. maps-search（地图）

问题：在上海搜索咖啡馆，推荐一个即可。

候选参考：合成青禾咖啡馆，合成路18号，来源为模拟高德返回。

必须工具：maps_text_search。
禁止工具：set_reminder。

关键词断言：合成青禾咖啡馆。
来源断言：0条指定原文。

- [ ] 有对应地点卡片；不补造未返回的营业时间/价格。
- [ ] 业务预期已确认（当前待审核）。

### 15. maps-around（地图）

问题：我提供的高德坐标是121.500000,31.300000，搜索附近1000米的咖啡馆。

候选参考：基于用户给定坐标及1000米范围返回合成青禾咖啡馆。

必须工具：maps_around_search。
禁止工具：set_reminder。

关键词断言：合成青禾咖啡馆。
来源断言：0条指定原文。

- [ ] location与用户提供值一致；radius规范化为字符串1000；不得用虚构current定位。
- [ ] 业务预期已确认（当前待审核）。

### 16. maps-detail（地图）

问题：先搜索上海的咖啡馆，再查第一家的营业时间和人均消费。

候选参考：先取得BTEST001，再查详情；09:00-21:00，人均35元。

必须工具：maps_text_search, maps_search_detail。
禁止工具：set_reminder。

关键词断言：09:00, 21:00, 35。
来源断言：0条指定原文。

- [ ] 详情ID必须来自本题搜索结果；不从无详情的搜索结果编造信息。
- [ ] 业务预期已确认（当前待审核）。

### 17. maps-geocode-walk（地图）

问题：查询从上海合成路1号到合成路18号的步行路线。

候选参考：先地理编码确认两端坐标，再查询步行路线；模拟距离1200米、耗时900秒（15分钟）。

必须工具：maps_geo, maps_direction_walking。
禁止工具：set_reminder。

关键词断言：无；人工核对。
来源断言：0条指定原文。

- [ ] 两端坐标来自地理编码；距离1200米/1.2公里、时间15分钟/900秒；有路线卡片。
- [ ] 业务预期已确认（当前待审核）。

### 18. maps-multistep（地图）

问题：我的高德坐标是121.500000,31.300000。请在上海搜索咖啡馆，查第一家的营业时间，再规划从我这里步行过去的路线。

候选参考：搜索→BTEST001详情→步行路线；09:00-21:00，1200米、15分钟。

必须工具：maps_text_search, maps_search_detail, maps_direction_walking。
禁止工具：set_reminder。

关键词断言：合成青禾咖啡馆, 09:00。
来源断言：0条指定原文。

- [ ] 工具顺序和数据依赖正确；累计token及耗时包含发现轮次；不补造位置。
- [ ] 业务预期已确认（当前待审核）。

### 19. boundary-no-location（边界）

问题：附近有什么好吃的？

候选参考：当前没有位置，先询问城市或具体位置，不猜测所在城市。

必须工具：respond。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：无；人工核对。
来源断言：0条指定原文。

- [ ] 可以调用tool_search了解能力；不能调用地图业务工具；实际答复必须询问位置，不能只说“没有”。
- [ ] 业务预期已确认（当前待审核）。

### 20. boundary-no-booking（边界）

问题：帮我预订明晚七点的餐厅座位，直接替我下单。

候选参考：说明当前无法直接预订或下单，可询问城市并提供查找帮助。

必须工具：respond。
禁止工具：maps_text_search, maps_around_search, maps_search_detail, maps_weather, maps_geo, maps_direction_walking, maps_direction_driving, maps_direction_bicycling, maps_direction_transit_integrated, set_reminder。

关键词断言：无；人工核对。
来源断言：0条指定原文。

- [ ] 不得声称已预订、已付款或创建提醒；不能虚构不存在的预订工具。
- [ ] 业务预期已确认（当前待审核）。

## 当前能力边界

- 工具断言只检查调用名称，不自动判断参数、执行顺序或提醒是否真的符合语义；这些需按上表核对原始报告。
- 地图录制缺失表示参数未被本套件覆盖，需区分合理等价写法与真正错误。
- 合成题通过只能支持开发比较，不能推断真实业务总体成功率。
