const { test, expect } = require("@playwright/test");
const { extract } = require("./sandpost");

test("sign up with the code from the mail", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Email").fill("new.user@example.test");
  await page.getByRole("button", { name: "Sign up" }).click();

  // Waits for the mail of this run, then reads the code from it.
  const code = await extract({ query: "to:new.user@example.test" });

  await page.getByLabel("Code").fill(code);
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page.getByRole("heading", { name: "Welcome" })).toBeVisible();
});
