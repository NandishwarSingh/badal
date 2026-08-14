// Time helpers for the timetable. Slots recur by weekday + minutes-from-
// midnight in the timetable's timezone, so "now" is computed against that
// wall clock rather than against a UTC instant.

export const DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
export const DAYS_LONG = [
  "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday",
];

// Current weekday (0 = Monday) and minutes since midnight, read in `tz`.
// Reading the wall clock in the timetable's own zone keeps a slot at 09:00
// meaning 09:00 in Kolkata even if the device is set to another timezone.
export function nowIn(tz = "Asia/Kolkata") {
  let d;
  try {
    d = new Date(new Date().toLocaleString("en-US", { timeZone: tz }));
    if (Number.isNaN(d.getTime())) d = new Date();
  } catch {
    d = new Date();
  }
  return { dow: (d.getDay() + 6) % 7, min: d.getHours() * 60 + d.getMinutes() };
}

// 545 -> "09:05". Minutes past 1440 wrap to the next day's clock, so a block
// ending at 1500 reads "01:00".
export function hhmm(min) {
  const m = ((min % 1440) + 1440) % 1440;
  return `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
}

// "1h 20m" / "45m" / "3h" for a duration in minutes.
export function dur(min) {
  const h = Math.floor(min / 60), m = min % 60;
  if (h && m) return `${h}h ${m}m`;
  if (h) return `${h}h`;
  return `${m}m`;
}

// A human gap to a start time from now, both in minutes: "in 25 min",
// "in 2h 10m", or "now".
export function until(min) {
  if (min <= 0) return "now";
  if (min < 60) return `in ${min} min`;
  const h = Math.floor(min / 60), m = min % 60;
  return m ? `in ${h}h ${m}m` : `in ${h}h`;
}

// Status of one slot relative to (dow, min): "past" | "now" | "next" | "later".
// A slot from the previous day whose ends_min exceeds 1440 is still "now"
// during the early hours of the following day.
export function statusOf(slot, dow, min) {
  const startsToday = slot.day_of_week === dow;
  const isTail = slot.ends_min > 1440 && (slot.day_of_week + 1) % 7 === dow;
  if (isTail && min < slot.ends_min - 1440) return "now";
  if (!startsToday) return "later";
  if (min >= slot.starts_min && min < slot.ends_min) return "now";
  if (min >= slot.ends_min) return "past";
  return "upcoming";
}

// Today's slots in clock order, plus a still-running tail from yesterday
// pinned to the front so an overnight block does not vanish after midnight.
export function slotsForDay(slots, dow) {
  const today = slots
    .filter((s) => s.day_of_week === dow)
    .sort((a, b) => a.starts_min - b.starts_min);
  const tail = slots.filter(
    (s) => s.ends_min > 1440 && (s.day_of_week + 1) % 7 === dow,
  );
  return [...tail, ...today];
}

// The next slot to begin at or after `min` today; failing that, the first slot
// of the soonest following day that has any. Returns { slot, inMin } or null.
export function nextSlot(slots, dow, min) {
  const laterToday = slots
    .filter((s) => s.day_of_week === dow && s.starts_min > min)
    .sort((a, b) => a.starts_min - b.starts_min);
  if (laterToday.length) {
    return { slot: laterToday[0], inMin: laterToday[0].starts_min - min };
  }
  for (let step = 1; step <= 7; step++) {
    const d = (dow + step) % 7;
    const day = slots
      .filter((s) => s.day_of_week === d)
      .sort((a, b) => a.starts_min - b.starts_min);
    if (day.length) {
      return { slot: day[0], inMin: step * 1440 - min + day[0].starts_min };
    }
  }
  return null;
}
