import dayjs from "dayjs";
import duration from "dayjs/plugin/duration";
import DayJSRelativeTime from "dayjs/plugin/relativeTime";

dayjs.extend(duration);
dayjs.extend(DayJSRelativeTime);

export type TimeUnit = "days" | "hours";

export function humanDuration(durationInMs: number) {
	if (durationInMs === 0) {
		return "0 hours";
	}

	const timeUnit = suggestedTimeUnit(durationInMs);
	const durationValue =
		timeUnit === "days"
			? durationInDays(durationInMs)
			: durationInHours(durationInMs);

	return `${durationValue} ${timeUnit}`;
}

export function suggestedTimeUnit(duration: number): TimeUnit {
	if (duration === 0) {
		return "hours";
	}

	return Number.isInteger(durationInDays(duration)) ? "days" : "hours";
}

export function durationInHours(duration: number): number {
	return duration / 1000 / 60 / 60;
}

export function durationInDays(duration: number): number {
	return duration / 1000 / 60 / 60 / 24;
}

export function relativeTime(date: Date) {
	return dayjs(date).fromNow();
}

export function daysAgo(count: number) {
	const date = new Date();
	date.setDate(date.getDate() - count);
	return date.toISOString();
}
