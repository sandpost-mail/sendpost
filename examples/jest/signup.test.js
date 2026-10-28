const { sendVerificationMail } = require("./app");
const { extract } = require("./sandpost");

// Mail may arrive after the test starts: extract waits up to 30 s for it.
jest.setTimeout(60_000);

test("the verification mail carries the code", async () => {
  await sendVerificationMail("new.user@example.test", "482913");
  expect(await extract({ query: "subject:verify" })).toBe("482913");
});

test("the verification mail carries the link", async () => {
  await sendVerificationMail("link.user@example.test", "731904");
  const link = await extract({ query: "to:link.user@example.test", link: "/verify" });
  expect(link).toBe("https://app.example.test/verify?code=731904");
});
