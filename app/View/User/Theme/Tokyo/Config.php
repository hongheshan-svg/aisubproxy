<?php
declare(strict_types=1);

namespace App\View\User\Theme\Tokyo;

use App\Consts\Render;

/**
 * Tokyo 风格模板：胶囊顶栏 + 左侧分类 + 右侧表格式商品列表。
 *
 * 商城前台（Index/*）与登录注册（Authentication/*）是 Tokyo 自己写的页面；
 * 会员中心页面（User/Dashboard/Agent + Common/Footer、SecurityNav）是 Cartoon 原样复制，
 * 只换了 Common/Header.html（顶栏）并叠加 Assets/User.css，用 scripts/sync-tokyo-usercenter.sh 同步。
 * 登录注册与会员中心跟随后台「PC会员中心」模板设置，需要在那里也选上 Tokyo。
 */
interface Config
{
    const INFO = [
        "NAME" => "Tokyo",
        "AUTHOR" => "local",
        "VERSION" => "1.0.0",
        "WEB_SITE" => "#",
        "DESCRIPTION" => "胶囊顶栏、左侧分类、表格式商品列表的简洁白色模板",
        "RENDER" => Render::ENGINE_SMARTY
    ];

    const SUBMIT = [
        [
            "title" => "公告弹窗",
            "name" => "notice_popup",
            "type" => "switch",
            "text" => "进入首页时弹出公告（点“我已阅读”后 1 小时内不再弹出）"
        ],
        [
            "title" => "顶栏附加链接",
            "name" => "nav_links",
            "type" => "textarea",
            "height" => 120,
            "placeholder" => "每行一个：名称|链接|图标(可选，FontAwesome 类名)\n例：充值优惠|/user/recharge/index|fa-gift"
        ],
        [
            "title" => "ICP备案号",
            "name" => "icp",
            "type" => "input",
            "placeholder" => "填写后将会在店铺底部显示ICP备案号，不填写则不显示。"
        ]
    ];

    const THEME = [
        "INDEX" => "Index/Index.html",
        "ITEM" => "Index/Item.html",
        "QUERY" => "Index/Query.html",

        "LOGIN" => "Authentication/Login.html",
        "REGISTER" => "Authentication/Register.html",
        "FORGET_EMAIL" => "Authentication/ForgetEmail.html",
        "FORGET_PHONE" => "Authentication/ForgetPhone.html",

        "DASHBOARD" => "Dashboard/Index.html",
        "RECHARGE" => "User/Recharge.html",
        "BILL" => "User/Bill.html",
        "BUSINESS" => "User/Business.html",
        "CATEGORY" => "User/Category.html",
        "COMMODITY" => "User/Commodity.html",
        "CARD" => "User/Card.html",
        "COUPON" => "User/Coupon.html",
        "CASH" => "User/Cash.html",
        "CASH_RECORD" => "User/CashRecord.html",
        "PERSONAL" => "User/Personal.html",
        "EMAIL" => "User/Email.html",
        "PHONE" => "User/Phone.html",
        "PASSWORD" => "User/Password.html",
        "TWO_FACTOR" => "User/TwoFactor.html",
        "PASSKEY" => "User/Passkey.html",
        "DEVICE" => "User/Device.html",
        "IP_WHITELIST" => "User/IpWhitelist.html",
        "SECURITY_LOG" => "User/SecurityLog.html",
        "ORDER" => "User/Order.html",
        "PURCHASE_RECORD" => "User/PurchaseRecord.html",
        "TICKET" => "User/Ticket.html",
        "TICKET_CREATE" => "User/TicketCreate.html",
        "TICKET_DETAIL" => "User/TicketDetail.html",
        "MESSAGE" => "User/Message.html",
        "AGENT_MEMBER" => "Agent/Member.html",
        "AGENT_PROMOTE" => "Agent/Promote.html",
    ];
}
