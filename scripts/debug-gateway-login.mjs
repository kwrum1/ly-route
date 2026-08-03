import { chromium } from 'playwright';

const browser = await chromium.launch({ channel: 'chrome', headless: true });
const page = await browser.newPage({ ignoreHTTPSErrors: true });
page.on('console', (message) => console.log(`console:${message.type()}: ${message.text()}`));
page.on('pageerror', (error) => console.log(`pageerror: ${error.stack || error.message}`));
page.on('request', (request) => console.log(`request: ${request.method()} ${request.url()}`));
page.on('response', (response) => console.log(`response: ${response.status()} ${response.url()}`));
await page.goto('https://127.0.0.1:8445/', { waitUntil: 'networkidle' });
console.log(`loginForm=${await page.locator('#loginForm').count()} appHidden=${await page.locator('#appShell').evaluate((node) => node.classList.contains('is-hidden'))}`);
await page.locator('#username').fill('admin');
await page.locator('#password').fill('LyRouteDemo2026!');
await page.locator('#loginForm button[type="submit"]').click();
await page.waitForTimeout(2000);
console.log(`url=${page.url()} appHidden=${await page.locator('#appShell').evaluate((node) => node.classList.contains('is-hidden'))}`);
await browser.close();
