# 地区数据来源

`goofish_regions.json` 原样复制自 [usagi-org/ai-goofish-monitor 的地区选项快照](https://github.com/usagi-org/ai-goofish-monitor/blob/ec35dccff8ebd64365e12c36fd1a579674f0778d/web-ui/src/data/goofishRegions.json)，提交为 `ec35dccff8ebd64365e12c36fd1a579674f0778d`，SHA-256 为 `10ea7178fe6ba5007943934b61cef94ae65a2a9eec267b4f423d0d100b062fea`。

上游采用 MIT 许可证，完整许可与版权声明保存在同目录的 `LICENSE`，原许可见[上游文件](https://github.com/usagi-org/ai-goofish-monitor/blob/77f8d75869acd65f9dba72731b95c7c49ea67bdf/LICENSE)。

这份数据表示网页筛选选项，不替换成另一套行政区划：保留原文、原顺序、全国和城市群、全选项、海外及直辖市的特殊层级。节点编号由层级和完整路径组成，同名选项不会串到其他父级。

市名反查排除全选项、城市群和海外；北京、天津、上海、重庆只用省市同名的身份，不把其第二列的区县当作唯一市名。列表和保存校验仍保留全部第二列区县。
