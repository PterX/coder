import type { Meta, StoryObj } from "@storybook/react";
import { expect, spyOn, userEvent, waitFor, within } from "@storybook/test";
import { API } from "api/api";
import { MockUsers } from "pages/UsersPage/storybookData/users";
import { reactRouterParameters } from "storybook-addon-remix-react-router";
import {
	MockAIPromptPresets,
	MockNewTaskData,
	MockPresets,
	MockTasks,
	MockTemplate,
	MockTemplateVersionExternalAuthGithub,
	MockTemplateVersionExternalAuthGithubAuthenticated,
	MockUserOwner,
	mockApiError,
} from "testHelpers/entities";
import {
	withAuthProvider,
	withGlobalSnackbar,
	withProxyProvider,
} from "testHelpers/storybook";
import TasksPage, { data } from "./TasksPage";

const meta: Meta<typeof TasksPage> = {
	title: "pages/TasksPage",
	component: TasksPage,
	decorators: [withAuthProvider],
	parameters: {
		user: MockUserOwner,
		permissions: {
			viewDeploymentConfig: true,
		},
	},
	beforeEach: () => {
		spyOn(API, "getTemplateVersionExternalAuth").mockResolvedValue([]);
		spyOn(API, "getTemplateVersionPresets").mockResolvedValue(null);
		spyOn(API, "getUsers").mockResolvedValue({
			users: MockUsers,
			count: MockUsers.length,
		});
		spyOn(data, "fetchAITemplates").mockResolvedValue([
			MockTemplate,
			{
				...MockTemplate,
				id: "test-template-2",
				name: "template 2",
				display_name: "Template 2",
			},
		]);
	},
};

export default meta;
type Story = StoryObj<typeof TasksPage>;

export const LoadingAITemplates: Story = {
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockImplementation(
			() => new Promise(() => 1000 * 60 * 60),
		);
	},
};

export const LoadingAITemplatesError: Story = {
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockRejectedValue(
			mockApiError({
				message: "Failed to load AI templates",
				detail: "You don't have permission to access this resource.",
			}),
		);
	},
};

export const EmptyAITemplates: Story = {
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([]);
	},
};

export const LoadingTasks: Story = {
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockImplementation(
			() => new Promise(() => 1000 * 60 * 60),
		);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Select the first AI template", async () => {
			const form = await canvas.findByRole("form");
			const combobox = await within(form).findByRole("combobox");
			expect(combobox).toHaveTextContent(MockTemplate.display_name);
		});
	},
};

export const LoadingTasksError: Story = {
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockRejectedValue(
			mockApiError({
				message: "Failed to load tasks",
			}),
		);
	},
};

export const EmptyTasks: Story = {
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockResolvedValue([]);
	},
};

export const LoadedTasks: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockResolvedValue(MockTasks);
	},
};

export const LoadedTasksWithPresets: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		const mockTemplateWithPresets = {
			...MockTemplate,
			id: "test-template-2",
			name: "template-with-presets",
			display_name: "Template with Presets",
		};

		spyOn(data, "fetchAITemplates").mockResolvedValue([
			MockTemplate,
			mockTemplateWithPresets,
		]);
		spyOn(data, "fetchTasks").mockResolvedValue(MockTasks);
		spyOn(API, "getTemplateVersionPresets").mockImplementation(
			async (versionId) => {
				// Return presets only for the second template
				if (versionId === mockTemplateWithPresets.active_version_id) {
					return MockPresets;
				}
				return null;
			},
		);
	},
};

export const LoadedTasksWithAIPromptPresets: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		const mockTemplateWithPresets = {
			...MockTemplate,
			id: "test-template-2",
			name: "template-with-presets",
			display_name: "Template with AI Prompt Presets",
		};

		spyOn(data, "fetchAITemplates").mockResolvedValue([
			MockTemplate,
			mockTemplateWithPresets,
		]);
		spyOn(data, "fetchTasks").mockResolvedValue(MockTasks);
		spyOn(API, "getTemplateVersionPresets").mockImplementation(
			async (versionId) => {
				// Return presets only for the second template
				if (versionId === mockTemplateWithPresets.active_version_id) {
					return MockAIPromptPresets;
				}
				return null;
			},
		);
	},
};

export const LoadedTasksEdgeCases: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockResolvedValue(MockTasks);

		// Test various edge cases for presets
		spyOn(API, "getTemplateVersionPresets").mockImplementation(async () => {
			return [
				{
					ID: "malformed",
					Name: "Malformed Preset",
					Default: true,
				},
				// biome-ignore lint/suspicious/noExplicitAny: Testing malformed data edge cases
			] as any;
		});
	},
};

export const CreateTaskSuccessfully: Story = {
	decorators: [withProxyProvider()],
	parameters: {
		reactRouter: reactRouterParameters({
			location: {
				path: "/tasks",
			},
			routing: [
				{
					path: "/tasks",
					useStoryElement: true,
				},
				{
					path: "/tasks/:ownerName/:workspaceName",
					element: <h1>Task page</h1>,
				},
			],
		}),
	},
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks")
			.mockResolvedValueOnce(MockTasks)
			.mockResolvedValue([MockNewTaskData, ...MockTasks]);
		spyOn(data, "createTask").mockResolvedValue(MockNewTaskData);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Run task", async () => {
			const prompt = await canvas.findByLabelText(/prompt/i);
			await userEvent.type(prompt, MockNewTaskData.prompt);
			const submitButton = canvas.getByRole("button", { name: /run task/i });
			await waitFor(() => expect(submitButton).toBeEnabled());
			await userEvent.click(submitButton);
		});

		await step("Redirects to the task page", async () => {
			await canvas.findByText(/task page/i);
		});
	},
};

export const CreateTaskError: Story = {
	decorators: [withProxyProvider(), withGlobalSnackbar],
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockResolvedValue(MockTasks);
		spyOn(data, "createTask").mockRejectedValue(
			mockApiError({
				message: "Failed to create task",
				detail: "You don't have permission to create tasks.",
			}),
		);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Run task", async () => {
			const prompt = await canvas.findByLabelText(/prompt/i);
			await userEvent.type(prompt, "Create a new task");
			const submitButton = canvas.getByRole("button", { name: /run task/i });
			await waitFor(() => expect(submitButton).toBeEnabled());
			await userEvent.click(submitButton);
		});

		await step("Verify error", async () => {
			await canvas.findByText(/failed to create task/i);
		});
	},
};

export const WithAuthenticatedExternalAuth: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		spyOn(data, "fetchTasks")
			.mockResolvedValueOnce(MockTasks)
			.mockResolvedValue([MockNewTaskData, ...MockTasks]);
		spyOn(data, "createTask").mockResolvedValue(MockNewTaskData);
		spyOn(API, "getTemplateVersionExternalAuth").mockResolvedValue([
			MockTemplateVersionExternalAuthGithubAuthenticated,
		]);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Does not render external auth", async () => {
			expect(
				canvas.queryByText(/external authentication/),
			).not.toBeInTheDocument();
		});
	},
	parameters: {
		chromatic: {
			disableSnapshot: true,
		},
	},
};

export const MissingExternalAuth: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		spyOn(data, "fetchTasks")
			.mockResolvedValueOnce(MockTasks)
			.mockResolvedValue([MockNewTaskData, ...MockTasks]);
		spyOn(data, "createTask").mockResolvedValue(MockNewTaskData);
		spyOn(API, "getTemplateVersionExternalAuth").mockResolvedValue([
			MockTemplateVersionExternalAuthGithub,
		]);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Submit is disabled", async () => {
			const prompt = await canvas.findByLabelText(/prompt/i);
			await userEvent.type(prompt, MockNewTaskData.prompt);
			const submitButton = canvas.getByRole("button", { name: /run task/i });
			expect(submitButton).toBeDisabled();
		});

		await step("Renders external authentication", async () => {
			await canvas.findByRole("button", { name: /connect to github/i });
		});
	},
};

export const ExternalAuthError: Story = {
	decorators: [withProxyProvider()],
	beforeEach: () => {
		spyOn(data, "fetchTasks")
			.mockResolvedValueOnce(MockTasks)
			.mockResolvedValue([MockNewTaskData, ...MockTasks]);
		spyOn(data, "createTask").mockResolvedValue(MockNewTaskData);
		spyOn(API, "getTemplateVersionExternalAuth").mockRejectedValue(
			mockApiError({
				message: "Failed to load external auth",
			}),
		);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Submit is disabled", async () => {
			const prompt = await canvas.findByLabelText(/prompt/i);
			await userEvent.type(prompt, MockNewTaskData.prompt);
			const submitButton = canvas.getByRole("button", { name: /run task/i });
			expect(submitButton).toBeDisabled();
		});

		await step("Renders error", async () => {
			await canvas.findByText(/failed to load external auth/i);
		});
	},
};

export const NonAdmin: Story = {
	decorators: [withProxyProvider()],
	parameters: {
		permissions: {
			viewDeploymentConfig: false,
		},
	},
	beforeEach: () => {
		spyOn(data, "fetchAITemplates").mockResolvedValue([MockTemplate]);
		spyOn(data, "fetchTasks").mockResolvedValue(MockTasks);
	},
	play: async ({ canvasElement, step }) => {
		const canvas = within(canvasElement);

		await step("Can't see filters", async () => {
			await canvas.findByRole("table");
			expect(
				canvas.queryByRole("region", { name: /filters/i }),
			).not.toBeInTheDocument();
		});
	},
};
