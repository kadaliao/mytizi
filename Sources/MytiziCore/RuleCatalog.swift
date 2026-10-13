import Foundation

/// 内置域名清单。全部使用 DOMAIN-SUFFIX（纯域名匹配，不依赖 DNS 解析结果）。
///
/// 为什么不能只靠 GEOIP,CN 兜底（来自同类工具的踩坑记录）：
///   - 国内平台的图片/视频 CDN 会通过 HTTPDNS、多 CDN 调度返回不同地区的 IP，
///     GEOIP 判定会在 DIRECT 和代理之间漂移，表现为"接口正常、图片/视频反复失败"。
///   - 微信图片依赖自家 HTTPDNS 取 CDN 地址后按 IP 直连，必须保持直连。
///   - 飞书 API 与它的图片 CDN（feishucdn.com、byteimg.com 等）必须走同一个出口。
///   - 海外媒体（YouTube、Gemini 生成视频、Claude）在国内 DNS 污染最严重，
///     解析结果一旦被判成 CN 就会直连失败，所以用域名固定走代理。
public enum RuleCatalog {
    public struct Group: Equatable {
        public let name: String
        public let domains: [String]
    }

    // MARK: 国内平台与 CDN → 直连

    public static let directGroups: [Group] = [
        Group(name: "腾讯 / 微信 / QQ", domains: [
            "qq.com", "qq.com.cn", "weixin.com", "wechat.com", "wechatapp.com", "weixinbridge.com",
            "qpic.cn", "qlogo.cn", "gtimg.com", "gtimg.cn", "idqqimg.com", "url.cn",
            "myqcloud.com", "qcloud.com", "tencent.com", "tencent-cloud.com", "tencentcs.com",
            "tencentyun.com", "tencentmusic.com", "tenpay.com", "weiyun.com", "foxmail.com",
            "cdn-go.cn", "sogou.com",
        ]),
        Group(name: "阿里 / 淘宝 / 钉钉", domains: [
            "alibaba.com", "alibabacloud.com", "alibaba-inc.com", "aliyun.com", "aliyuncs.com",
            "alicdn.com", "alipay.com", "alipayobjects.com", "taobao.com", "tmall.com",
            "tbcdn.cn", "taobaocdn.com", "mmstat.com", "1688.com", "dingtalk.com",
            "dingtalkapps.com", "aliyundrive.com", "alipan.com", "quark.cn", "uc.cn",
            "ucweb.com", "youku.com", "ykimg.com", "amap.com", "autonavi.com", "cainiao.com",
            "ele.me", "eleme.cn", "fliggy.com", "alikunlun.com", "alikunlun.net",
        ]),
        Group(name: "字节 / 抖音 / 飞书", domains: [
            "bytedance.com", "bytedance.net", "douyin.com", "douyinpic.com", "douyinvod.com",
            "douyincdn.com", "douyinstatic.com", "iesdouyin.com", "byteimg.com", "ibyteimg.com",
            "ibytedtos.com", "bytecdn.cn", "bytegoofy.com", "ixigua.com", "toutiao.com",
            "snssdk.com", "pstatp.com", "zijieapi.com", "zjcdn.com", "volces.com",
            "volcengine.com", "volccdn.com", "amemv.com", "jianying.com",
            "feishu.cn", "feishucdn.com", "feishupkg.com", "larksuite.com", "larkoffice.com",
            "larksuitecdn.com",
        ]),
        Group(name: "百度", domains: [
            "baidu.com", "bdstatic.com", "bdimg.com", "bcebos.com", "baidubce.com",
            "baidupcs.com", "baidustatic.com", "baiducontent.com",
        ]),
        Group(name: "哔哩哔哩", domains: [
            "bilibili.com", "bilivideo.com", "bilivideo.cn", "hdslb.com", "biliapi.net",
            "biliapi.com", "acgvideo.com", "b23.tv", "biligame.com",
        ]),
        Group(name: "网易", domains: [
            "163.com", "126.net", "127.net", "netease.com", "163yun.com", "netease.im",
            "kaola.com", "youdao.com",
        ]),
        Group(name: "微博 / 新浪", domains: [
            "weibo.com", "weibo.cn", "sinaimg.cn", "sina.com.cn", "sina.cn", "sinajs.cn", "t.cn",
        ]),
        Group(name: "小红书 / 快手", domains: [
            "xiaohongshu.com", "xhscdn.com", "xhslink.com",
            "kuaishou.com", "kwimgs.com", "yximgs.com", "gifshow.com", "ksapisrv.com",
            "kuaishouzt.com", "ksyun.com", "ksyuncs.com",
        ]),
        Group(name: "电商 / 本地生活", domains: [
            "jd.com", "360buyimg.com", "jdpay.com", "meituan.com", "meituan.net", "dianping.com",
            "dpfile.com", "sankuai.com", "pinduoduo.com", "pddpic.com", "yangkeduo.com",
            "ctrip.com", "xiaomi.com", "mi.com", "miui.com", "mi-img.com", "miwifi.com",
        ]),
        Group(name: "社区 / 内容 / 视频", domains: [
            "zhihu.com", "zhimg.com", "csdn.net", "csdnimg.cn",
            "cnblogs.com", "juejin.cn", "jianshu.com", "jianshu.io", "gitee.com", "oschina.net",
            "ximalaya.com", "xmcdn.com", "iqiyi.com", "qiyipic.com", "71.am", "mgtv.com",
            "hitv.com", "sohu.com", "itc.cn", "ifeng.com",
        ]),
        Group(name: "手机厂商 / 运营商 / 支付", domains: [
            "huawei.com", "hicloud.com", "huaweicloud.com", "dbankcdn.com", "vmall.com",
            "hihonor.com", "oppo.com", "heytapmobi.com", "vivo.com.cn",
            "10086.cn", "chinamobile.com", "10010.com", "chinaunicom.com", "189.cn", "ctyun.cn",
            "unionpay.com",
        ]),
        Group(name: "国内 CDN 与云厂商", domains: [
            "qiniucdn.com", "qbox.me", "qnssl.com", "qiniudn.com", "qiniu.com",
            "upyun.com", "upaiyun.com", "ucloud.cn", "chinacache.net", "chinanetcenter.com",
            "wscdns.com", "cdn20.com", "ctdns.net", "ourdvsss.com", "ourwebcdn.com",
            "ourwebpic.com", "tbcache.com", "myhuaweicloud.com",
        ]),
    ]

    public static var directDomains: [String] { directGroups.flatMap(\.domains) }

    // MARK: 海外固定走代理

    /// 这些域名即使 GeoSite 数据库过期或 DNS 被污染也要走代理，所以排在 GEOSITE 之前。
    public static let proxyGroups: [Group] = [
        // usercontent.google.com 承载 Gemini 生成的媒体，已被 google.com 覆盖；
        // googleusercontent.com 是另一个独立域名，必须单独保留。
        Group(name: "Google / YouTube / Gemini", domains: [
            "google.com", "googleapis.com", "gstatic.com", "googleusercontent.com",
            "ytimg.com", "ggpht.com", "youtube.com", "youtu.be", "googlevideo.com",
        ]),
        Group(name: "Claude / OpenAI", domains: [
            "claude.ai", "claude.com", "anthropic.com", "claudeusercontent.com",
            "openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
        ]),
        Group(name: "开发 / 社交 / 资讯", domains: [
            "github.com", "githubusercontent.com", "githubassets.com", "twitter.com", "x.com",
            "twimg.com", "t.co", "facebook.com", "fbcdn.net", "instagram.com", "whatsapp.com",
            "whatsapp.net", "telegram.org", "t.me", "telegra.ph", "wikipedia.org",
            "wikimedia.org", "reddit.com", "redd.it", "redditmedia.com", "discord.com",
            "discordapp.com", "discord.gg", "netflix.com", "nflxvideo.net", "spotify.com",
            "scdn.co",
        ]),
    ]

    public static var proxyDomains: [String] { proxyGroups.flatMap(\.domains) }

    // MARK: 本地 / 内网

    public static let privateIPv4: [String] = [
        "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
        "192.168.0.0/16", "100.64.0.0/10", "224.0.0.0/4",
    ]

    // MARK: DNS 相关

    /// 这些域名不使用 fake-ip：时间同步、STUN、连通性检测、本地域名需要真实地址。
    public static let fakeIPFilter: [String] = [
        "*.lan", "*.local", "*.localdomain", "*.home.arpa", "localhost.ptlogin2.qq.com",
        "time.*.com", "time.*.gov", "time.*.edu.cn", "ntp.*.com", "*.ntp.org.cn", "+.pool.ntp.org",
        "stun.*.*", "stun.*.*.*", "+.stun.*.*", "+.stun.*.*.*",
        "+.msftconnecttest.com", "+.msftncsi.com", "captive.apple.com", "+.captive.apple.com",
        "+.srv.nintendo.net", "+.stun.playstation.net", "xbox.*.microsoft.com", "+.xboxlive.com",
    ]

    /// 嗅探器不改写目标地址的域名。微信按 HTTPDNS 给出的 IP 直连，SNI/Host 只是占位，
    /// 改写会把下载请求送到别的 CDN 集群，表现为图片一直转圈。
    public static let snifferSkip: [String] = [
        "+.weixin.qq.com", "+.weixin.qq.com.cn", "+.push.apple.com",
    ]

    /// 国内 DNS，仅用于 DIRECT 流量与 GEOIP 兜底的解析。明文 UDP 在绝大多数网络都可用，
    /// 不依赖 DoH 证书与时间，是"小白网络环境"下最稳的选择。
    public static let domesticDNS: [String] = ["223.5.5.5", "119.29.29.29"]
}
