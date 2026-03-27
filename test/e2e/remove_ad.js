// 模拟墨鱼风格的去广告脚本
// 精确删除 JSON 响应中的广告字段，保留正常内容
var body = JSON.parse($response.body);

if (body.data) {
    // 删除广告列表
    if (body.data.ad_list) {
        delete body.data.ad_list;
    }
    // 删除开屏广告
    if (body.data.splash_ad) {
        delete body.data.splash_ad;
    }
}

$done({body: JSON.stringify(body)});
