import { test } from "@playwright/test";
import { users } from "../../constants";
import {
	buildWorkspaceWithParameters,
	createTemplate,
	createWorkspace,
	disableDynamicParameters,
	echoResponsesWithParameters,
	stopWorkspace,
	verifyParameters,
} from "../../helpers";
import { login } from "../../helpers";
import { beforeCoderTest } from "../../hooks";
import { firstBuildOption, secondBuildOption } from "../../parameters";
import type { RichParameter } from "../../provisionerGenerated";

test.beforeEach(async ({ page }) => {
	beforeCoderTest(page);
});

test("start workspace with ephemeral parameters", async ({ page }) => {
	await login(page, users.templateAdmin);
	const richParameters: RichParameter[] = [firstBuildOption, secondBuildOption];
	const template = await createTemplate(
		page,
		echoResponsesWithParameters(richParameters),
	);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	const workspaceName = await createWorkspace(page, template);

	// Verify that build options are default (not selected).
	await verifyParameters(page, workspaceName, richParameters, [
		{ name: richParameters[0].name, value: firstBuildOption.defaultValue },
		{ name: richParameters[1].name, value: secondBuildOption.defaultValue },
	]);

	// Stop the workspace
	await stopWorkspace(page, workspaceName);

	// Now, start the workspace with ephemeral parameters selected.
	const buildParameters = [
		{ name: richParameters[0].name, value: "AAAAA" },
		{ name: richParameters[1].name, value: "true" },
	];

	await buildWorkspaceWithParameters(
		page,
		workspaceName,
		richParameters,
		buildParameters,
	);

	// Verify that build options are default (not selected).
	await verifyParameters(page, workspaceName, richParameters, [
		{ name: richParameters[0].name, value: firstBuildOption.defaultValue },
		{ name: richParameters[1].name, value: secondBuildOption.defaultValue },
	]);
});
