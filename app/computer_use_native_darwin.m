#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ImageIO/ImageIO.h>
#import <dispatch/dispatch.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

static NSDictionary *umcAppJSON(NSRunningApplication *app) {
    return @{ @"name": app.localizedName ?: @"", @"bundle_id": app.bundleIdentifier ?: @"",
              @"pid": @(app.processIdentifier), @"path": app.bundleURL.path ?: @"" };
}

static NSRunningApplication *umcFindApp(NSDictionary *target) {
    pid_t pid = [target[@"pid"] intValue];
    NSString *bundle = target[@"bundle_id"] ?: @"", *name = target[@"name"] ?: @"", *path = target[@"path"] ?: @"";
    for (NSRunningApplication *app in NSWorkspace.sharedWorkspace.runningApplications) {
        if ((pid > 0 && app.processIdentifier == pid) ||
            (bundle.length && [app.bundleIdentifier isEqualToString:bundle]) ||
            (name.length && [app.localizedName caseInsensitiveCompare:name] == NSOrderedSame) ||
            (path.length && [app.bundleURL.path isEqualToString:path])) return app;
    }
    return nil;
}

static NSDictionary *umcWindow(pid_t pid) {
    CFArrayRef raw = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
    NSArray *windows = CFBridgingRelease(raw);
    for (NSDictionary *window in windows) {
        if ([window[(id)kCGWindowOwnerPID] intValue] != pid || [window[(id)kCGWindowLayer] intValue] != 0) continue;
        CGRect bounds = CGRectZero;
        CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)window[(id)kCGWindowBounds], &bounds);
        if (bounds.size.width < 2 || bounds.size.height < 2) continue;
        return @{ @"id": window[(id)kCGWindowNumber] ?: @0, @"title": window[(id)kCGWindowName] ?: @"",
                  @"x": @(bounds.origin.x), @"y": @(bounds.origin.y), @"width": @(bounds.size.width), @"height": @(bounds.size.height) };
    }
    return nil;
}

static id umcCopyAttribute(AXUIElementRef element, CFStringRef attribute) {
    CFTypeRef raw = NULL;
    if (AXUIElementCopyAttributeValue(element, attribute, &raw) != kAXErrorSuccess || !raw) return nil;
    return CFBridgingRelease(raw);
}

static void umcCollectControls(AXUIElementRef element, NSString *path, NSUInteger depth,
                              CGRect windowBounds, CGFloat scaleX, CGFloat scaleY,
                              NSMutableArray *out, NSUInteger *count) {
    if (depth > 8 || *count >= 250) return;
    id role = umcCopyAttribute(element, kAXRoleAttribute);
    id title = umcCopyAttribute(element, kAXTitleAttribute);
    id description = umcCopyAttribute(element, kAXDescriptionAttribute);
    id help = umcCopyAttribute(element, kAXHelpAttribute);
    id identifier = umcCopyAttribute(element, kAXIdentifierAttribute);
    id enabled = umcCopyAttribute(element, kAXEnabledAttribute);
    id focused = umcCopyAttribute(element, kAXFocusedAttribute);
    NSMutableDictionary *node = [@{ @"id": path, @"enabled": enabled ?: @YES, @"focused": focused ?: @NO } mutableCopy];
    if ([role isKindOfClass:NSString.class] && [role length]) node[@"role"] = role;
    id label = [title isKindOfClass:NSString.class] && [title length] ? title :
               ([description isKindOfClass:NSString.class] && [description length] ? description : help);
    if ([label isKindOfClass:NSString.class] && [label length]) node[@"label"] = label;
    if ([identifier isKindOfClass:NSString.class] && [identifier length]) node[@"identifier"] = identifier;

    id positionObject = umcCopyAttribute(element, kAXPositionAttribute);
    id sizeObject = umcCopyAttribute(element, kAXSizeAttribute);
    AXValueRef rawPosition = (__bridge AXValueRef)positionObject;
    AXValueRef rawSize = (__bridge AXValueRef)sizeObject;
    CGPoint position = CGPointZero; CGSize size = CGSizeZero;
    BOOL hasPosition = rawPosition && CFGetTypeID(rawPosition) == AXValueGetTypeID() && AXValueGetValue(rawPosition, kAXValueCGPointType, &position);
    BOOL hasSize = rawSize && CFGetTypeID(rawSize) == AXValueGetTypeID() && AXValueGetValue(rawSize, kAXValueCGSizeType, &size);
    if (hasPosition && hasSize && size.width > 0 && size.height > 0) {
        node[@"x"] = @((position.x - windowBounds.origin.x) * scaleX);
        node[@"y"] = @((position.y - windowBounds.origin.y) * scaleY);
        node[@"width"] = @(size.width * scaleX);
        node[@"height"] = @(size.height * scaleY);
    }
    [out addObject:node]; (*count)++;

    CFTypeRef rawChildren = NULL;
    if (AXUIElementCopyAttributeValue(element, kAXChildrenAttribute, &rawChildren) != kAXErrorSuccess || !rawChildren) return;
    NSArray *children = CFBridgingRelease(rawChildren);
    NSUInteger childCount = MIN(children.count, 250 - *count);
    for (NSUInteger i = 0; i < childCount && *count < 250; i++) {
        AXUIElementRef child = (__bridge AXUIElementRef)children[i];
        umcCollectControls(child, [path stringByAppendingFormat:@"/%lu", (unsigned long)i], depth + 1,
                          windowBounds, scaleX, scaleY, out, count);
    }
}

static NSArray *umcControls(pid_t pid, NSDictionary *window, NSDictionary *pixels) {
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    CFTypeRef rawWindows = NULL;
    NSMutableArray *out = [NSMutableArray array];
    if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &rawWindows) == kAXErrorSuccess && rawWindows) {
        NSArray *windows = (__bridge NSArray *)rawWindows;
        CGRect bounds = CGRectMake([window[@"x"] doubleValue], [window[@"y"] doubleValue], [window[@"width"] doubleValue], [window[@"height"] doubleValue]);
        CGFloat scaleX = bounds.size.width > 0 ? [pixels[@"pixel_width"] doubleValue] / bounds.size.width : 1;
        CGFloat scaleY = bounds.size.height > 0 ? [pixels[@"pixel_height"] doubleValue] / bounds.size.height : 1;
        NSUInteger selectedIndex = NSNotFound;
        CGFloat bestDistance = CGFLOAT_MAX;
        for (NSUInteger i = 0; i < windows.count; i++) {
            AXUIElementRef axWindow = (__bridge AXUIElementRef)windows[i];
            id p = umcCopyAttribute(axWindow, kAXPositionAttribute), z = umcCopyAttribute(axWindow, kAXSizeAttribute);
            CGPoint origin = CGPointZero; CGSize size = CGSizeZero;
            BOOL valid = p && z && AXValueGetValue((__bridge AXValueRef)p, kAXValueCGPointType, &origin) && AXValueGetValue((__bridge AXValueRef)z, kAXValueCGSizeType, &size);
            if (valid) {
                CGFloat distance = fabs(origin.x - bounds.origin.x) + fabs(origin.y - bounds.origin.y) +
                                   fabs(size.width - bounds.size.width) + fabs(size.height - bounds.size.height);
                if (distance < bestDistance) { bestDistance = distance; selectedIndex = i; }
            }
        }
        if (selectedIndex != NSNotFound && bestDistance < MAX(bounds.size.width, bounds.size.height)) {
            NSUInteger count = 0;
            AXUIElementRef axWindow = (__bridge AXUIElementRef)windows[selectedIndex];
            umcCollectControls(axWindow, [NSString stringWithFormat:@"w%lu", (unsigned long)selectedIndex], 0,
                               bounds, scaleX, scaleY, out, &count);
        }
        CFRelease(rawWindows);
    }
    CFRelease(app);
    return out;
}

static AXUIElementRef umcResolveControl(pid_t pid, NSString *controlID) {
    NSArray *parts = [controlID componentsSeparatedByString:@"/"];
    if (parts.count < 2 || ![parts[0] hasPrefix:@"w"]) return NULL;
    NSInteger windowIndex = [[parts[0] substringFromIndex:1] integerValue];
    if (windowIndex < 0) return NULL;
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    CFTypeRef rawWindows = NULL;
    if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &rawWindows) != kAXErrorSuccess || !rawWindows) { CFRelease(app); return NULL; }
    NSArray *windows = (__bridge NSArray *)rawWindows;
    if ((NSUInteger)windowIndex >= windows.count) { CFRelease(rawWindows); CFRelease(app); return NULL; }
    // Keep each control alive independently of the array returned by AX.
    // A deep path otherwise uses a child after its parent array is released.
    AXUIElementRef current = (AXUIElementRef)CFRetain((__bridge CFTypeRef)windows[(NSUInteger)windowIndex]);
    for (NSUInteger partIndex = 1; partIndex < parts.count; partIndex++) {
        NSInteger childIndex = [parts[partIndex] integerValue];
        CFTypeRef rawChildren = NULL;
        if (childIndex < 0 || AXUIElementCopyAttributeValue(current, kAXChildrenAttribute, &rawChildren) != kAXErrorSuccess || !rawChildren) {
            CFRelease(current); CFRelease(rawWindows); CFRelease(app); return NULL;
        }
        NSArray *children = (__bridge NSArray *)rawChildren;
        if ((NSUInteger)childIndex >= children.count) { CFRelease(rawChildren); CFRelease(current); CFRelease(rawWindows); CFRelease(app); return NULL; }
        AXUIElementRef next = (AXUIElementRef)CFRetain((__bridge CFTypeRef)children[(NSUInteger)childIndex]);
        CFRelease(rawChildren);
        CFRelease(current);
        current = next;
    }
    CFRelease(rawWindows); CFRelease(app);
    return current;
}

static BOOL umcWriteScreenshot(NSDictionary *window, NSString *path, NSDictionary **pixels, NSString **error) {
    if (!CGPreflightScreenCaptureAccess()) {
        // Request from the UMCode desktop process itself. This makes macOS
        // register and display UMCode (rather than a former standalone helper)
        // in the Screen Recording privacy list.
        if (!CGRequestScreenCaptureAccess() || !CGPreflightScreenCaptureAccess()) {
            *error = @"Screen Recording permission is required for UMCode. Enable UMCode in System Settings → Privacy & Security → Screen Recording, then retry.";
            return NO;
        }
    }
    CGWindowID windowID = [window[@"id"] unsignedIntValue];
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    CGImageRef image = CGWindowListCreateImage(CGRectNull, kCGWindowListOptionIncludingWindow, windowID, kCGWindowImageBoundsIgnoreFraming);
#pragma clang diagnostic pop
    if (!image) { *error = @"UMCode could not capture the selected app window."; return NO; }
    size_t width = CGImageGetWidth(image), height = CGImageGetHeight(image);
    CGImageDestinationRef dest = CGImageDestinationCreateWithURL((__bridge CFURLRef)[NSURL fileURLWithPath:path], CFSTR("public.png"), 1, NULL);
    if (!dest) { CGImageRelease(image); *error = @"UMCode could not create the screenshot artifact."; return NO; }
    CGImageDestinationAddImage(dest, image, NULL);
    BOOL ok = CGImageDestinationFinalize(dest);
    CFRelease(dest); CGImageRelease(image);
    if (!ok) { *error = @"UMCode could not write the screenshot artifact."; return NO; }
    *pixels = @{ @"pixel_width": @(width), @"pixel_height": @(height) };
    return YES;
}

static void umcClick(CGPoint point, BOOL twice) {
    for (int i = 1; i <= (twice ? 2 : 1); i++) {
        CGEventRef down = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, point, kCGMouseButtonLeft);
        CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, point, kCGMouseButtonLeft);
        CGEventSetIntegerValueField(down, kCGMouseEventClickState, i);
        CGEventSetIntegerValueField(up, kCGMouseEventClickState, i);
        // The selected app is activated before reaching this path. Posting to
        // the HID event tap gives Chromium and other apps the same global
        // pointer events they receive from a physical click; process-directed
        // CGEventPostToPid events were accepted but could be ignored by web
        // content, leaving the action report looking successful with no UI
        // change.
        CGEventPost(kCGHIDEventTap, down); CGEventPost(kCGHIDEventTap, up);
        CFRelease(down); CFRelease(up);
    }
}

static void umcMove(CGPoint point) {
    CGEventRef current = CGEventCreate(NULL);
    CGPoint origin = current ? CGEventGetLocation(current) : point;
    if (current) CFRelease(current);
    // A short bounded glide makes the actual system cursor legible to the
    // person watching the target app, while the in-app preview mirrors it.
    for (int step = 1; step <= 12; step++) {
        CGFloat t = (CGFloat)step / 12.0;
        CGPoint next = CGPointMake(origin.x + (point.x - origin.x) * t, origin.y + (point.y - origin.y) * t);
        CGEventRef moved = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, next, kCGMouseButtonLeft);
        CGEventPost(kCGHIDEventTap, moved);
        CFRelease(moved);
        usleep(8000);
    }
}

// Manager.Act converts screenshot-pixel coordinates into window-local display
// points before calling the native driver. CoreGraphics mouse events need
// global display points, so only add the current window origin here. Scaling
// again here would shrink Retina coordinates twice and make successful clicks
// land far above/left of their intended target.
static BOOL umcActionPoint(NSDictionary *window, NSDictionary *action, CGPoint *point, NSString **error) {
    CGFloat x = [window[@"x"] doubleValue], y = [window[@"y"] doubleValue];
    CGFloat width = [window[@"width"] doubleValue], height = [window[@"height"] doubleValue];
    CGFloat localX = [action[@"x"] doubleValue], localY = [action[@"y"] doubleValue];
    if (width <= 0 || height <= 0 || localX < 0 || localY < 0 || localX >= width || localY >= height) {
        *error = @"UMCode received invalid target window dimensions.";
        return NO;
    }
    *point = CGPointMake(x + localX, y + localY);
    return YES;
}

static void umcType(pid_t pid, NSString *text) {
    NSUInteger length = text.length;
    UniChar *chars = calloc(length, sizeof(UniChar));
    [text getCharacters:chars range:NSMakeRange(0, length)];
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true), up = CGEventCreateKeyboardEvent(NULL, 0, false);
    CGEventKeyboardSetUnicodeString(down, length, chars); CGEventKeyboardSetUnicodeString(up, length, chars);
    CGEventPostToPid(pid, down); CGEventPostToPid(pid, up);
    CFRelease(down); CFRelease(up); free(chars);
}

static CGKeyCode umcKeyCode(NSString *key) {
    NSDictionary *codes = @{ @"return": @36, @"enter": @36, @"tab": @48, @"space": @49, @"escape": @53,
        @"left": @123, @"right": @124, @"down": @125, @"up": @126, @"delete": @51, @"backspace": @51,
        @"home": @115, @"end": @119 };
    NSNumber *code = codes[key.lowercaseString];
    return code ? code.unsignedShortValue : UINT16_MAX;
}

static void umcPostCommandA(pid_t pid) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true), up = CGEventCreateKeyboardEvent(NULL, 0, false);
    CGEventSetFlags(down, kCGEventFlagMaskCommand); CGEventSetFlags(up, kCGEventFlagMaskCommand);
    CGEventPostToPid(pid, down); CGEventPostToPid(pid, up); CFRelease(down); CFRelease(up);
}

static NSString *umcExecute(NSString *command, NSDictionary *input) {
    if ([command isEqualToString:@"list"]) {
        NSMutableArray *apps = [NSMutableArray array];
        for (NSRunningApplication *app in NSWorkspace.sharedWorkspace.runningApplications)
            if (app.activationPolicy == NSApplicationActivationPolicyRegular && app.localizedName.length) [apps addObject:umcAppJSON(app)];
        return [NSString.alloc initWithData:[NSJSONSerialization dataWithJSONObject:@{@"apps": apps} options:0 error:nil] encoding:NSUTF8StringEncoding];
    }
    NSDictionary *target = input[@"target"] ?: @{};
    NSRunningApplication *app = umcFindApp(target);
    if (!app) return @"ERROR:The selected application is not running.";
    NSDictionary *window = umcWindow(app.processIdentifier);
    if (!window) return @"ERROR:The selected application has no visible window.";
    if ([command isEqualToString:@"inspect"]) {
        NSString *path = input[@"screenshot"];
        if (!path.length) return @"ERROR:A screenshot path is required.";
        NSDictionary *pixels = nil; NSString *error = nil;
        if (!umcWriteScreenshot(window, path, &pixels, &error)) return [@"ERROR:" stringByAppendingString:error];
        NSMutableDictionary *windowState = [window mutableCopy]; [windowState addEntriesFromDictionary:pixels];
        BOOL accessibility = AXIsProcessTrusted();
        NSDictionary *state = @{@"app": umcAppJSON(app), @"window": windowState,
            @"permission": accessibility ? @"ready" : @"screen-only; Accessibility permission is required for actions",
            @"controls": accessibility ? umcControls(app.processIdentifier, window, pixels) : @[]};
        NSData *data = [NSJSONSerialization dataWithJSONObject:state options:0 error:nil];
        return [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    }
    if (![command isEqualToString:@"act"]) return @"ERROR:Unknown Computer Use command.";
    if (!AXIsProcessTrusted()) return @"ERROR:Accessibility permission is required for UMCode. Enable UMCode in System Settings → Privacy & Security → Accessibility, then retry.";
    // UMCode can remain the frontmost app while the model is reasoning. A
    // process-targeted event alone is not sufficient for every app (notably
    // Chromium) when its window is in the background. Bring the selected app
    // forward before acting, then resolve its actual front window again.
    if (!app.isActive) {
        [app activateWithOptions:NSApplicationActivateIgnoringOtherApps];
        usleep(120000);
        window = umcWindow(app.processIdentifier);
        if (!window) return @"ERROR:The selected application has no visible window after activation.";
    }
    NSDictionary *action = input[@"action"] ?: @{};
    NSString *type = action[@"type"] ?: @"";
    NSString *elementID = action[@"element_id"] ?: @"";
    pid_t pid = app.processIdentifier;
    CGPoint point = CGPointZero;
    NSString *pointError = nil;
    BOOL hasPoint = action[@"x"] != nil && action[@"y"] != nil;
    BOOL needsPoint = [type isEqualToString:@"move"] || [type isEqualToString:@"click"] || [type isEqualToString:@"double_click"] || [type isEqualToString:@"fill"] || [type isEqualToString:@"scroll"];
    if (needsPoint && hasPoint && !umcActionPoint(window, action, &point, &pointError)) return [@"ERROR:" stringByAppendingString:pointError ?: @"Could not map target coordinates."];
    if (needsPoint && !hasPoint && !elementID.length) return @"ERROR:This action needs screenshot coordinates or an accessibility control id.";
    AXUIElementRef control = elementID.length ? umcResolveControl(pid, elementID) : NULL;
    if (elementID.length && !control) return @"ERROR:The accessibility control is no longer available. Inspect the app and choose a current control.";
    if ([type isEqualToString:@"move"]) {
        if (!hasPoint) { if (control) CFRelease(control); return @"ERROR:This accessibility control has no visible bounds for pointer movement."; }
        umcMove(point);
    } else if ([type isEqualToString:@"click"]) {
        if (hasPoint) umcMove(point);
        if (!control || AXUIElementPerformAction(control, kAXPressAction) != kAXErrorSuccess) {
            if (!hasPoint) { if (control) CFRelease(control); return @"ERROR:Accessibility press failed and no coordinate fallback is available."; }
            umcClick(point, NO);
        }
    } else if ([type isEqualToString:@"double_click"]) {
        if (!hasPoint) { if (control) CFRelease(control); return @"ERROR:Double-click requires screenshot coordinates."; }
        umcMove(point); umcClick(point, YES);
    } else if ([type isEqualToString:@"fill"]) {
        if (hasPoint) umcMove(point);
        BOOL filled = NO;
        if (control) {
            Boolean settable = false;
            if (AXUIElementIsAttributeSettable(control, kAXValueAttribute, &settable) == kAXErrorSuccess && settable)
                filled = AXUIElementSetAttributeValue(control, kAXValueAttribute, (__bridge CFTypeRef)(action[@"text"] ?: @"")) == kAXErrorSuccess;
        }
        if (!filled) {
            if (!hasPoint) { if (control) CFRelease(control); return @"ERROR:This control cannot be filled through Accessibility and has no coordinate fallback."; }
            umcClick(point, NO); usleep(60000); umcPostCommandA(pid); usleep(30000); umcType(pid, action[@"text"] ?: @"");
        }
    }
    if ([type isEqualToString:@"type"]) {
        if (control) AXUIElementSetAttributeValue(control, kAXFocusedAttribute, kCFBooleanTrue);
        umcType(pid, action[@"text"] ?: @"");
    }
    else if ([type isEqualToString:@"key"]) { NSString *key = action[@"key"] ?: @""; if ([key.lowercaseString isEqualToString:@"cmd+a"] || [key.lowercaseString isEqualToString:@"command+a"]) umcPostCommandA(pid); else { CGKeyCode code = umcKeyCode(key); if (code == UINT16_MAX) return @"ERROR:Unsupported key."; CGEventRef d = CGEventCreateKeyboardEvent(NULL, code, true), u = CGEventCreateKeyboardEvent(NULL, code, false); CGEventPostToPid(pid, d); CGEventPostToPid(pid, u); CFRelease(d); CFRelease(u); } }
    else if ([type isEqualToString:@"scroll"]) { CGEventRef event = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 1, [action[@"delta"] intValue]); CGEventSetLocation(event, point); CGEventPostToPid(pid, event); CFRelease(event); }
    else if (![type isEqualToString:@"move"] && ![type isEqualToString:@"click"] && ![type isEqualToString:@"double_click"] && ![type isEqualToString:@"fill"]) { if (control) CFRelease(control); return @"ERROR:Unsupported Computer Use action."; }
    if (control) CFRelease(control);
    return @"{\"ok\":true}";
}

char *umcComputerUse(const char *command, const char *input, char **error) {
    __block char *output = NULL;
    dispatch_block_t execute = ^{
        @autoreleasepool {
            NSData *data = [NSData dataWithBytes:input length:strlen(input)];
            NSDictionary *json = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
            NSString *result = umcExecute([NSString stringWithUTF8String:command], [json isKindOfClass:NSDictionary.class] ? json : @{});
            if ([result hasPrefix:@"ERROR:"]) {
                *error = strdup([[result substringFromIndex:6] UTF8String]);
                return;
            }
            output = strdup(result.UTF8String ?: "{}");
        }
    };

    // The HTTP bridge is served from a Go worker goroutine. AppKit activation
    // and Accessibility APIs must be called from the app's main thread; doing
    // so from the worker can close the bridge connection before the engine
    // receives an HTTP response (reported as EOF on computer.act).
    if ([NSThread isMainThread]) execute();
    else dispatch_sync(dispatch_get_main_queue(), execute);
    return output;
}
