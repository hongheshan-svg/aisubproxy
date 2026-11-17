'use strict';
const assert = require('node:assert/strict');
require('../browser-runtime');
const { chromium } = require('playwright-extra');
const { ensureCardEntryReady } = require('../card-entry');

(async () => {
    const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_TEST_EXECUTABLE || undefined });
    try {
        for (const scenario of ['saved', 'hosted', 'selected-but-disabled', 'failed-click']) {
            const context = await browser.newContext();
            await context.route('**/*', async route => {
                if (new URL(route.request().url()).hostname === 'chatgpt.com') {
                    return route.fulfill({ contentType: 'text/html', body: '<iframe style="width:800px;height:500px" src="https://js.stripe.com/fixture/payment"></iframe>' });
                }
                const immediate = scenario === 'hosted';
                const selected = scenario === 'selected-but-disabled';
                await route.fulfill({ contentType: 'text/html', body: `
                    <button hidden>Card</button><button id="saved" role="tab">Saved</button>
                    <button id="card" role="tab" aria-selected="${selected}">Card</button>
                    <button onclick="window.otherClicks++">Subscribe</button>
                    <button onclick="window.otherClicks++">Apple Pay</button>
                    <fieldset id="fields" style="display:${immediate || selected ? 'block' : 'none'}">
                        <input autocomplete="cc-number" ${selected ? 'disabled' : ''}>
                        <input autocomplete="cc-exp" ${selected ? 'disabled' : ''}>
                        <input autocomplete="cc-csc" ${!immediate ? 'disabled' : ''}>
                    </fieldset>
                    <script>
                    window.cardClicks=0;window.otherClicks=0;
                    document.getElementById('card').onclick=()=>{
                        window.cardClicks++;
                        if(${JSON.stringify(scenario)}==='failed-click') return;
                        document.getElementById('card').setAttribute('aria-selected','true');
                        setTimeout(()=>document.getElementById('fields').style.display='block',150);
                        setTimeout(()=>document.querySelector('[autocomplete="cc-csc"]').disabled=false,550);
                    };
                    </script>` });
            });
            const page = await context.newPage();
            await page.goto('https://chatgpt.com/checkout/fixture');
            let error;
            try {
                const fields = await ensureCardEntryReady(page, { timeoutMs: 1800, pollMs: 100 });
                for (const field of Object.values(fields)) assert.equal(await field.isEditable(), true);
            } catch (caught) { error = caught; }
            const frame = page.frames().find(f => f.url().includes('js.stripe.com'));
            const actions = await frame.evaluate(() => ({ card: window.cardClicks, other: window.otherClicks }));
            assert.equal(actions.other, 0);
            if (scenario === 'saved' || scenario === 'hosted') {
                if (error) throw error;
                assert.equal(actions.card, scenario === 'saved' ? 1 : 0);
            } else assert.equal(error?.code, 'card_entry_unavailable');
            console.log(JSON.stringify({ scenario, passed: true, actions }));
            await context.close();
        }
    } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
