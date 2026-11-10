package fake

import (
	"sync"
	"time"
)

// FilterBehavior 控制假页面的一步筛选，模拟点击没有请求、请求不返回或接口报错。
type FilterBehavior struct {
	NoRequest    bool          `json:"no_request"`
	NoResponse   bool          `json:"no_response"`
	Delay        time.Duration `json:"delay"`
	Ret          string        `json:"ret"`
	CorruptPrice bool          `json:"corrupt_price"`
}

type filterState struct {
	sync.Mutex
	behavior      map[string]FilterBehavior
	actions       []string
	emptyDistrict bool
}

func (f *Xianyu) SetFilterBehavior(step string, behavior FilterBehavior) {
	f.filters.Lock()
	defer f.filters.Unlock()
	if f.filters.behavior == nil {
		f.filters.behavior = map[string]FilterBehavior{}
	}
	f.filters.behavior[step] = behavior
}

func (f *Xianyu) SetRegionWithoutDistrict(on bool) {
	f.filters.Lock()
	defer f.filters.Unlock()
	f.filters.emptyDistrict = on
}

func (f *Xianyu) FilterActions() []string {
	f.filters.Lock()
	defer f.filters.Unlock()
	return append([]string(nil), f.filters.actions...)
}

const filterMarkup = `
<input placeholder="¥" value="99" aria-label="无关输入框" style="width:45px">
<div class="ant-popover" style="position:absolute;left:650px;top:150px;background:white"><div class="areaWrap--other"><div><div class="provItem--other">广东</div></div><div><div class="provItem--other">深圳</div></div><div><div class="provItem--other">南山</div></div></div><button class="searchBtn--other">查看999+件宝贝</button></div>
<span id="shipping" style="padding:8px">包邮</span><span id="region" style="padding:8px">区域</span>
<div class="search-price-input-container--test"><input placeholder="最低 ¥" style="width:100px;height:30px"><input placeholder="最高 ¥" style="width:100px;height:30px"></div>
<div class="ant-popover" id="region-popup" style="display:none;position:absolute;top:100px;left:30px;background:white;z-index:10;padding:10px">
 <div class="areaWrap--test" style="display:flex;gap:15px"><div id="provinces" style="width:120px"></div><div id="cities" style="width:120px"></div><div id="districts" style="width:120px"></div></div>
 <div>重新定位</div><button class="searchBtn--test">查看2件宝贝</button>
</div>`

const filterScript = `<script>
const behavior = FILTERBEHAVIOR;
const emptyDistrict = EMPTYDISTRICT;
const selected = {personal:false, shipping:false, province:'', city:'', district:''};
const priceInputs = document.querySelector('[class*="search-price-input-container"]').querySelectorAll('[placeholder*="¥"]');
const minimum = priceInputs[0];
const maximum = priceInputs[1];
function filter(step, extra='') {
  const query = 'step=' + step + '&min=' + encodeURIComponent(minimum.value) + '&max=' + encodeURIComponent(maximum.value) + '&' + extra;
  fetch('/fixture/action?' + query, {method:'POST'});
  if (behavior && behavior[step] && behavior[step].no_request) return;
  return search(query);
}
document.getElementById('personal').addEventListener('click', () => { selected.personal = !selected.personal; filter('personal', 'enabled=' + Number(selected.personal)); });
document.getElementById('shipping').addEventListener('click', () => { selected.shipping = !selected.shipping; filter('shipping', 'enabled=' + Number(selected.shipping)); });
const popup = document.getElementById('region-popup');
document.getElementById('region').addEventListener('click', () => { popup.style.display='block'; filter('region_open'); });
function choices(id, values, click) {
  const column = document.getElementById(id);
  column.replaceChildren();
  values.forEach(value => { const option = document.createElement('div'); option.className='provItem--test'; option.style.padding='7px'; option.textContent=value; option.addEventListener('click', () => click(value)); column.append(option); });
}
choices('provinces', ['全国','广东','浙江','上海','海外'], value => {
 selected.province=value; selected.city=''; selected.district='';
 choices('cities', ['全'+value, '深圳','广州','杭州','东莞','江浙沪'], city => {
  selected.city=city; selected.district='';
  choices('districts', emptyDistrict ? [] : ['全'+city,'南山','福田','西湖'], district => { selected.district=district; filter('region_district'); });
  filter('region_city');
 });
 filter('region_province');
});
document.querySelector('.searchBtn--test').addEventListener('click', () => { popup.style.display='none'; filter('region', 'province='+encodeURIComponent(selected.province)+'&city='+encodeURIComponent(selected.city)+'&district='+encodeURIComponent(selected.district)); });
[minimum, maximum].forEach(input => input.addEventListener('keydown', event => {
 if (event.key !== 'Tab' && event.key !== 'Enter') return;
 const step = event.key === 'Tab' ? 'price_tab' : 'price_enter';
 filter(step);
 if (!(behavior && behavior[step] && behavior[step].no_request)) {
   [minimum, maximum].forEach(field => { if (field.value !== '') field.value = Number(field.value).toFixed(2); });
   if (behavior && behavior[step] && behavior[step].corrupt_price) minimum.value = '1';
 }
}));
</script>`
