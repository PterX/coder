import Checkbox from "@mui/material/Checkbox";
import FormControlLabel from "@mui/material/FormControlLabel";
import TextField from "@mui/material/TextField";
import { ConfirmDialog } from "components/Dialogs/ConfirmDialog/ConfirmDialog";
import type { DialogProps } from "components/Dialogs/Dialog";
import { FormFields } from "components/Form/Form";
import { Stack } from "components/Stack/Stack";
import { useFormik } from "formik";
import type { PublishVersionData } from "pages/TemplateVersionEditorPage/types";
import type { FC } from "react";
import { getFormHelpers } from "utils/formUtils";
import * as Yup from "yup";
import {
	HelpTooltip,
	HelpTooltipContent,
	HelpTooltipLink,
	HelpTooltipLinksGroup,
	HelpTooltipText,
	HelpTooltipTitle,
	HelpTooltipTrigger,
} from "../../components/HelpTooltip/HelpTooltip";
import { docs } from "../../utils/docs";

export const Language = {
	versionNameLabel: "Version name",
	messagePlaceholder: "Write a short message about the changes you made...",
	defaultCheckboxLabel: "Promote to active version",
	activeVersionHelpTitle: "Active versions",
	activeVersionHelpText:
		"Templates can enforce that the active version be used for all workspaces (enterprise-only)",
	activeVersionHelpBody: "Review the documentation",
};

type PublishTemplateVersionDialogProps = DialogProps & {
	defaultName: string;
	isPublishing: boolean;
	publishingError?: unknown;
	onClose: () => void;
	onConfirm: (data: PublishVersionData) => void;
};

export const PublishTemplateVersionDialog: FC<
	PublishTemplateVersionDialogProps
> = ({
	onConfirm,
	isPublishing,
	onClose,
	defaultName,
	publishingError,
	...dialogProps
}) => {
	const form = useFormik({
		initialValues: {
			name: defaultName,
			message: "",
			isActiveVersion: true,
		},
		validationSchema: Yup.object({
			name: Yup.string().required(),
			message: Yup.string(),
			isActiveVersion: Yup.boolean(),
		}),
		onSubmit: onConfirm,
	});
	const getFieldHelpers = getFormHelpers(form, publishingError);
	const handleClose = () => {
		form.resetForm();
		onClose();
	};

	return (
		<ConfirmDialog
			{...dialogProps}
			confirmLoading={isPublishing}
			onClose={handleClose}
			onConfirm={async () => {
				await form.submitForm();
			}}
			hideCancel={false}
			type="success"
			cancelText="Cancel"
			confirmText="Publish"
			title="Publish new version"
			description={
				<form id="publish-version" onSubmit={form.handleSubmit}>
					<Stack>
						<p>You are about to publish a new version of this template.</p>
						<FormFields>
							<TextField
								{...getFieldHelpers("name")}
								label={Language.versionNameLabel}
								autoFocus
								disabled={isPublishing}
							/>

							<TextField
								{...getFieldHelpers("message")}
								label="Message"
								placeholder={Language.messagePlaceholder}
								disabled={isPublishing}
								multiline
								rows={5}
							/>

							<Stack direction={"row"}>
								<FormControlLabel
									label={Language.defaultCheckboxLabel}
									control={
										<Checkbox
											size="small"
											checked={form.values.isActiveVersion}
											onChange={async (e) => {
												await form.setFieldValue(
													"isActiveVersion",
													e.target.checked,
												);
											}}
											name="isActiveVersion"
										/>
									}
								/>

								<HelpTooltip>
									<HelpTooltipTrigger />

									<HelpTooltipContent>
										<HelpTooltipTitle>
											{Language.activeVersionHelpTitle}
										</HelpTooltipTitle>
										<HelpTooltipText>
											{Language.activeVersionHelpText}
										</HelpTooltipText>
										<HelpTooltipLinksGroup>
											<HelpTooltipLink
												href={docs(
													"/admin/templates/managing-templates#template-update-policies",
												)}
											>
												{Language.activeVersionHelpBody}
											</HelpTooltipLink>
										</HelpTooltipLinksGroup>
									</HelpTooltipContent>
								</HelpTooltip>
							</Stack>
						</FormFields>
					</Stack>
				</form>
			}
		/>
	);
};
