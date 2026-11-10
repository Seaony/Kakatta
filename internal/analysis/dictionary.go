package analysis

var riskDefinitions = [...]RiskDefinition{
	{"fake", "假货高仿", "真伪和来源", "confirm", "高仿、复刻、A 货、仿品，或图文和正品明显不符", "方便提供购买凭证或拍一下序列号吗？"},
	{"refurbished", "翻新", "真伪和来源", "mind", "翻新机、外壳或屏幕翻新", "这台有没有翻新过？"},
	{"official_replacement", "官换机", "真伪和来源", "mind", "官换机、官方换新机、售后换新", "是官换机吗？方便拍一下设置里的型号号码吗？"},
	{"resource_device", "资源机", "真伪和来源", "mind", "资源机、海外有锁机解锁、美版有锁", "是国行还是资源机？"},
	{"expanded", "扩容", "真伪和来源", "confirm", "扩容、改容量", "存储有没有扩容过？"},
	{"assembled", "拼装", "真伪和来源", "reject", "组装机、拼装机、配件拼凑", "是整机原装还是组装的？"},
	{"unknown_source", "来路不明", "真伪和来源", "mind", "无票无盒且说不清来源、公司发的、抵债来的", "这台是怎么来的，有购买记录吗？"},
	{"id_lock", "ID 锁", "锁和账号", "confirm", "Apple ID、小米账号、华为账号没退出，或有激活锁", "方便拍一下设置里账号已退出的截图吗？"},
	{"mdm", "监管机", "锁和账号", "confirm", "监管机、企业机、设备管理描述文件", "是监管机或企业设备吗？设置里有没有设备管理？"},
	{"carrier_lock", "网络锁", "锁和账号", "confirm", "卡贴机、有锁机、只能用某家运营商", "是无锁机吗？插任何卡都能用吗？"},
	{"account_bound", "账号绑定", "锁和账号", "mind", "游戏账号、会员、序列号绑在卖家账号上，转手不能换绑", "账号或授权能换绑到我名下吗？"},
	{"console_banned", "游戏机被封", "锁和账号", "confirm", "主机或账号被封、不能联网", "主机能正常联网吗？有没有被封过？"},
	{"repaired", "维修或拆修", "状态", "mind", "修过、拆过、换过主板或任何内部零件", "有没有拆修过？"},
	{"water_damage", "进水", "状态", "reject", "进水、受潮、进水标变色", "有没有进过水？"},
	{"screen_replaced", "换屏", "状态", "mind", "换过屏幕，原装屏或非原装屏", "屏幕是原装的吗？"},
	{"battery_replaced", "换电池", "状态", "mind", "换过电池", "电池换过吗？现在健康度多少？"},
	{"cosmetic_damage", "磕碰划痕", "状态", "mind", "磕碰、掉漆、明显划痕、裂痕", "外观有没有磕碰？方便拍一下四个角吗？"},
	{"malfunction", "功能故障", "状态", "reject", "有功能坏了：不开机、触摸失灵、摄像头、面容、指纹、按键、接口等", "所有功能都正常吗？"},
	{"missing_parts", "缺配件", "状态", "ignore", "缺原装充电器、线、盒子、说明书等", "配件都有哪些？"},
	{"stains", "脏污", "状态", "mind", "污渍、发黄、霉点", "有没有污渍？方便拍一下细节吗？"},
	{"pilling", "起球", "状态", "mind", "起球、磨损、勾丝", "有没有起球或磨损？"},
	{"odor", "异味", "状态", "mind", "烟味、霉味、宠物味、香水味", "有没有异味？家里有没有人抽烟或养宠物？"},
	{"washed", "洗过", "状态", "ignore", "洗过、干洗过", "洗过吗？"},
	{"mold", "发霉", "状态", "mind", "发霉、镜头起雾、霉斑", "镜头有没有霉、雾、灰？"},
	{"mining", "矿卡", "状态", "mind", "挖过矿、矿卡、长时间满载", "显卡是自己玩游戏用的吗？挖过矿吗？"},
	{"notes_marks", "笔记划线", "状态", "ignore", "书里有笔记、划线、签名", "书里有笔记或划线吗？"},
	{"size_mismatch", "尺码不符", "状态", "mind", "尺码和需求不符，或标的尺码和实际偏大偏小", "实际尺码是多少？平铺量一下行吗？"},
	{"photo_mismatch", "图文不一致", "描述可信度", "mind", "图里的东西和标题、描述对不上", "图片是这台实物吗？"},
	{"stock_photo", "网图", "描述可信度", "mind", "用官网图、网图、没有实物图", "方便拍几张实物图吗？"},
	{"vague", "关键信息含糊", "描述可信度", "mind", "描述很短、关键状态避而不谈、前后矛盾", "方便详细说一下成色和使用情况吗？"},
	{"off_platform", "站外交易", "交易方式", "reject", "要求加微信、QQ，或站外付款", ""},
	{"deposit", "先付定金", "交易方式", "reject", "要求先付定金、押金、诚意金", ""},
	{"pickup_only", "只能自提", "交易方式", "mind", "只能自提、只能面交", "可以走闲鱼发货吗？"},
	{"no_return", "不退不换", "交易方式", "ignore", "写明不退不换、售出不退", "收到后和描述不符可以退吗？"},
	{"no_inspection", "拒绝验货宝", "交易方式", "mind", "拒绝验货宝或官方验货", "可以走验货宝吗？"},
	{"seller_dealer", "疑似商家", "卖家", "mind", "由程序判断（5.8）", "是自己用的吗？用了多久？"},
	{"seller_new", "新号", "卖家", "mind", "由程序判断（5.8）", ""},
	{"seller_bad_reviews", "差评里有描述不符或售后差", "卖家", "mind", "由程序判断（5.8）", ""},
	{"seller_partial_refund", "被评到手刀", "卖家", "mind", "由程序判断（5.8）", ""},
}

func RiskDictionary() []RiskDefinition {
	return append([]RiskDefinition(nil), riskDefinitions[:]...)
}

func dictionaryRisk(id string) (RiskDefinition, bool) {
	for _, r := range riskDefinitions {
		if r.ID == id {
			return r, true
		}
	}
	return RiskDefinition{}, false
}

func generalRisk(id string) bool {
	switch id {
	case "photo_mismatch", "stock_photo", "vague", "off_platform", "deposit", "pickup_only", "no_return", "no_inspection", "seller_dealer", "seller_new", "seller_bad_reviews", "seller_partial_refund":
		return true
	}
	return false
}

func ResolveRisks(p Profile) []ResolvedRisk {
	result := make([]ResolvedRisk, 0, len(p.Risks))
	for _, r := range p.Risks {
		resolved := ResolvedRisk{ProfileRisk: r}
		if d, ok := dictionaryRisk(r.ID); ok {
			resolved.Name, resolved.Hint, resolved.Question, resolved.Category = d.Name, d.Hint, d.Question, d.Category
		} else if resolved.Hint == "" {
			resolved.Hint = resolved.Name
		}
		result = append(result, resolved)
	}
	return result
}

func DefaultProfile() Profile {
	p := Profile{Specs: []ProfileSpec{}, Musts: []Requirement{}, Bonuses: []Requirement{}, Risks: []ProfileRisk{}, AllowedKinds: []Kind{}, NextNo: 1}
	for _, d := range riskDefinitions {
		if generalRisk(d.ID) {
			p.Risks = append(p.Risks, ProfileRisk{ID: d.ID, Attitude: d.DefaultAttitude, Source: SourceSystem, Rev: 1})
		}
	}
	return p
}
